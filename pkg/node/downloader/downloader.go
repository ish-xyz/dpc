package downloader

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
)

var (
	killswitch KillSwitch
)

type KillSwitch struct {
	Trigger bool
	mu      sync.Mutex
}

type Downloader struct {
	Queue  chan *Item    `validate:"required"`
	Client *http.Client  `validate:"required"`
	Logger *logrus.Entry `validate:"required"`
	GC     *GC           `validate:"required"`
}

type Item struct {
	Req      *http.Request
	FilePath string
}

func NewDownloader(log *logrus.Entry, dataDir string, maxAtime, interval time.Duration, maxDiskUsage int) *Downloader {

	cache := &FilesCache{
		AtimeStore: make(map[string]int64),
		FilesByAge: make([]string, 1),
		FilesSize:  make(map[string]int64),
	}

	gc := &GC{
		MaxAtimeAge:  maxAtime,
		MaxDiskUsage: maxDiskUsage,
		Interval:     interval,
		DataDir:      dataDir,
		Logger:       log.WithField("component", "node.downloader.gc"),
		Cache:        cache,
		DryRun:       false,
	}

	return &Downloader{
		Queue:  make(chan *Item),
		Logger: log,
		Client: &http.Client{},
		GC:     gc,
	}
}

func (d *Downloader) Push(req *http.Request, filepath string) {

	item := &Item{
		Req:      req,
		FilePath: filepath,
	}
	d.Queue <- item
}

func (d *Downloader) Pop() *Item {
	return <-d.Queue
}

func (d *Downloader) download(item *Item) error {

	resp, err := d.Client.Do(item.Req)
	if err != nil {
		return fmt.Errorf("request error: %v", err)
	}
	defer resp.Body.Close()

	file, err := os.Create(item.FilePath)
	if err != nil {
		return err
	}
	defer file.Close()

	size, err := io.Copy(file, resp.Body)
	if err != nil {
		return fmt.Errorf("failed to memory copy into file")
	}

	if resp.Header.Get("content-length") != fmt.Sprintf("%d", size) {
		return fmt.Errorf("size mismatch, wanted %s actual %s", resp.Header.Get("content-length"), fmt.Sprintf("%d", size))
	}

	return err
}

func (d *Downloader) Run() {
	for {
		if killswitch.Trigger {
			d.Logger.Warningln("Max disk space reached, unable to download new files")
			continue
		}

		lastItem := d.Pop()
		err := d.download(lastItem)
		if err != nil {
			d.Logger.Errorf("failed to download item %s with error: %v", lastItem.FilePath, err)
			d.Logger.Infof("removing file %s", lastItem.FilePath)
			err = os.Remove(lastItem.FilePath)
			if err != nil {
				d.Logger.Errorf("failed to delete corrupt file %s with error %v", lastItem.FilePath, err)
			}
			continue
		}
		d.Logger.Infof("cached %s in %s", lastItem.Req.URL.String(), lastItem.FilePath)
	}
}

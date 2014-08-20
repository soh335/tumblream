package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	apiKey    = flag.String("apikey", "", "api key of tumblr")
	hostnames = flag.String("hostnames", "", "hostname of tumblr blog")
	dir       = flag.String("dir", "", "directory of output")
)

func main() {
	flag.Parse()

	absDir, err := filepath.Abs(*dir)
	if err != nil {
		log.Fatal(err)
	}

	saver := NewSaver(absDir)
	go saver.Run()

	agents := []*Agent{}
	for _, hostname := range strings.Split(*hostnames, ",") {
		agents = append(agents, &Agent{Hostname: hostname, ApiKey: *apiKey})
	}
	timer := time.NewTimer(0)

	if len(agents) == 0 {
		log.Fatal("empty agents")
	}

	for {
		select {
		case <-timer.C:
			var wg sync.WaitGroup
			for _, agent := range agents {
				wg.Add(1)
				go func(agent *Agent) {
					defer wg.Done()
					if err := agent.Run(saver.queue); err != nil {
						agent.Log("got err ", err, ". agent will be reset")
						agent.Reset()
					}
				}(agent)
			}
			wg.Wait()
			timer.Reset(time.Second * 60 * 30) // 1 hour
		}
	}
}

type TumblrResponse struct {
	Meta struct {
		Status int    `json:"status"`
		Msg    string `json:"msg"`
	} `json:"meta"`
	Response struct {
		Posts []struct {
			Id     int64                 `json:"id"`
			Photos []TumblrResponsePhoto `json:"photos"`
		} `json:"posts"`
	} `json:"response"`
}

type TumblrResponsePhoto struct {
	AltSizes []struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
		Url    string  `json:"url"`
	} `json:"alt_sizes"`
}

type Agent struct {
	lastId   int64
	Hostname string
	ApiKey   string
}

func (a *Agent) Reset() {
	a.lastId = 0
}

func (a *Agent) Log(v ...interface{}) {
	log.Println(fmt.Sprintf("[agent][%s]", a.Hostname), fmt.Sprint(v...))
}

func (a *Agent) Run(q chan<- string) error {
	a.Log("run")
	defer func() {
		a.Log("finished")
	}()

	offset := 0
	limit := 20
	var lastId int64

OUTER:
	for {
		resp, err := a.Fetch(limit, offset)
		if err != nil {
			return err
		}

		if len(resp.Response.Posts) < 1 {
			a.Log("not posts")
			break
		}

		if lastId == 0 {
			lastId = resp.Response.Posts[0].Id
		}

		// only set last id first time.
		if a.lastId == 0 {
			break
		}

		for _, post := range resp.Response.Posts {
			if a.lastId == post.Id {
				break OUTER
			}

			if a.lastId > post.Id {
				a.Log("seems to over last id", a.lastId, " > ", post.Id)
				break OUTER
			}

			for _, photo := range post.Photos {
				q <- photo.AltSizes[0].Url
			}
		}

		offset += limit
	}

	if lastId != 0 && a.lastId != lastId {
		a.Log("update last id ", a.lastId, " to ", lastId)
		a.lastId = lastId
	}

	return nil
}

func (a *Agent) Fetch(limit int, offset int) (*TumblrResponse, error) {
	u, err := url.Parse("https://api.tumblr.com/v2/blog/" + a.Hostname + "/posts/photo")
	if err != nil {
		return nil, err
	}

	v := u.Query()
	v.Set("api_key", a.ApiKey)
	v.Set("limit", strconv.Itoa(limit))
	v.Set("offset", strconv.Itoa(offset))

	u.RawQuery = v.Encode()

	a.Log("access to ", u.String())

	resp, err := http.Get(u.String())

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	var jsonResp TumblrResponse
	dec := json.NewDecoder(resp.Body)
	if err := dec.Decode(&jsonResp); err != nil {
		return nil, err
	}

	if jsonResp.Meta.Status != 200 {
		return nil, errors.New("tumblr error: " + jsonResp.Meta.Msg)
	}
	return &jsonResp, nil
}

type Saver struct {
	Dir   string
	queue chan string
}

func NewSaver(dir string) *Saver {
	s := &Saver{Dir: dir}
	s.queue = make(chan string)
	return s
}

func (s *Saver) Run() {
	for {
		url := <-s.queue
		go func(url string) {
			if err := s.Save(url); err != nil {
				log.Println(err)
			}
		}(url)
	}
}

func (s *Saver) Save(url string) error {
	splited := strings.Split(url, "/")
	fileName := filepath.Join(s.Dir, splited[len(splited)-1])

	resp, err := http.Get(url)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	file, err := os.OpenFile(fileName, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0666)
	if err != nil {
		if err.(*os.PathError).Err.Error() == "file exists" {
			s.Log(fileName, " is exists. so skip it.")
			return nil
		}
		return err
	}

	defer file.Close()

	if _, err := io.Copy(file, resp.Body); err != nil {
		return err
	}

	s.Log("saved ", url, " to ", file.Name())

	return nil
}

func (s *Saver) Log(v ...interface{}) {
	log.Println("[saver]", fmt.Sprint(v...))
}

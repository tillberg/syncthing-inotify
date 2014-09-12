// syncwatcher.go
package main

import (
  "code.google.com/p/go.exp/fsnotify"
  "fmt"
  "os"
  "bufio"
  "io/ioutil"
  "net/http"
  "encoding/json"
  "log"
  "flag"
  "net/url"
  "strings"
)


type Configuration struct {
  Version      int
  Repositories []RepositoryConfiguration
}

type RepositoryConfiguration struct {
  ID              string
  Directory       string
  ReadOnly        bool
  RescanIntervalS int
}


// HTTP Parameters
var (
  target    string
  authUser  string
  authPass  string
  csrfToken string
  csrfFile  string
  apiKey    string
)


func main() {
  flag.StringVar(&target, "target", "localhost:8080", "Target")
  flag.StringVar(&authUser, "user", "", "Username")
  flag.StringVar(&authPass, "pass", "", "Password")
  flag.StringVar(&csrfFile, "csrf", "", "CSRF token file")
  flag.StringVar(&apiKey, "api", "", "API key")
  flag.Parse()

  if len(csrfFile) > 0 {
    fd, err := os.Open(csrfFile)
    if err != nil {
      log.Fatal(err)
    }
    s := bufio.NewScanner(fd)
    for s.Scan() {
      csrfToken = s.Text()
    }
    fd.Close()
  }

  repos := getRepos()
  for i := range repos {
    repo := repos[i]
    go watchRepo(repo.ID, repo.Directory)
  }

  println("Press enter to exit")
  fmt.Scanln();
}

func getRepos() []RepositoryConfiguration {
  r, err := http.NewRequest("GET", "http://"+target+"/rest/config", nil)
  if err != nil {
    log.Fatal(err)
  }
  if len(csrfToken) > 0 {
    r.Header.Set("X-CSRF-Token", csrfToken)
  }
  if len(authUser) > 0 {
    r.SetBasicAuth(authUser, authPass)
  }
  if len(apiKey) > 0 {
    r.Header.Set("X-API-Key", apiKey)
  }
  res, err := http.DefaultClient.Do(r)
  if err != nil {
    log.Fatal(err)
  }
  defer res.Body.Close()
  if res.StatusCode != 200 {
    log.Fatalf("Status %d != 200 for GET", res.StatusCode)
  }
  bs, err := ioutil.ReadAll(res.Body)
  if err != nil {
    log.Fatal(err)
  }
  var cfg Configuration
  err = json.Unmarshal(bs, &cfg)
  if err != nil {
    log.Fatal(err)
  }
  return cfg.Repositories
}
 

func watchRepo(repo string, directory string) {
  sw, err := NewSyncWatcher()
  if sw == nil || err != nil {
    log.Fatal(err)
  }
  defer sw.Close()
  err = sw.Watch(directory)
  if err != nil {
    log.Fatal(err)
  }
  for {
    ev, ok := waitForEvent(sw)
    if ok && ev != nil {
      sub := strings.TrimPrefix(ev.Name, directory)
      sub = strings.TrimPrefix(sub, string(os.PathSeparator))
      informChange(repo, sub)
    }
  }
}

func waitForEvent(sw *SyncWatcher) (ev *fsnotify.FileEvent, ok bool) {
  select {
  case ev, ok = <-sw.Event:
    if !ok {
      log.Fatal("Event: channel closed")
    }
  case err, eok := <-sw.Error:
    log.Fatal(err, eok)
  }
  return
}

func informChange(repo string, sub string) {
  data := url.Values {}
  data.Set("repo", repo)
  data.Set("sub", sub)
  r, err := http.NewRequest("POST", "http://"+target+"/rest/scan?"+data.Encode(), nil)
  if err != nil {
    log.Fatal(err)
  }
  if len(csrfToken) > 0 {
    r.Header.Set("X-CSRF-Token", csrfToken)
  }
  if len(authUser) > 0 {
    r.SetBasicAuth(authUser, authPass)
  }
  if len(apiKey) > 0 {
    r.Header.Set("X-API-Key", apiKey)
  }
  res, err := http.DefaultClient.Do(r)
  if err != nil {
    log.Fatal(err)
  }
  defer res.Body.Close()
  if res.StatusCode != 200 {
    log.Fatalf("Status %d != 200 for POST", res.StatusCode)
  }
}

// syncwatcher.go
package main

import (
  "code.google.com/p/go.exp/fsnotify"
  "os"
  "bufio"
  "io/ioutil"
  "net/http"
  "encoding/json"
  "log"
  "fmt"
  "flag"
  "runtime"
  "path/filepath"
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

// Main
var (
	stop = make(chan int)
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
    repodir := repo.Directory
    repodir = expandTilde(repo.Directory)
    go watchRepo(repo.ID, repodir)
  }

  code := <-stop
  println("Exiting")
  os.Exit(code)

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
  log.Println("Watching "+repo+": "+directory)
  for {
    ev := waitForEvent(sw)
    if ev == nil {
      log.Fatal("fsnotify event is nil")
    }
    sub := strings.TrimPrefix(ev.Name, directory)
    sub = strings.TrimPrefix(sub, string(os.PathSeparator))
    informChange(repo, sub)
  }
}

func waitForEvent(sw *SyncWatcher) (ev *fsnotify.FileEvent) {
  var ok bool
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
  log.Println("Change detected in "+repo+": "+sub)
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
  } else {
    log.Println("Syncthing is indexing change in "+repo+": "+sub)
  }
}

func getHomeDir() string {
  var home string
  switch runtime.GOOS {
    case "windows":
    home = filepath.Join(os.Getenv("HomeDrive"), os.Getenv("HomePath"))
    if home == "" {
      home = os.Getenv("UserProfile")
    }
    default:
      home = os.Getenv("HOME")
  }
  if home == "" {
    log.Println("No home directory found - set $HOME (or the platform equivalent).")
  }
  return home
}

func expandTilde(p string) string {
  if p == "~" {
    return getHomeDir()
  }
  p = filepath.FromSlash(p)
  if !strings.HasPrefix(p, fmt.Sprintf("~%c", os.PathSeparator)) {
    return p
  }
  return filepath.Join(getHomeDir(), p[2:])
}

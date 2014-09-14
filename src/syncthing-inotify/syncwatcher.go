// syncwatcher.go
package main

import (
	"code.google.com/p/go.exp/fsnotify"
	"os"
	"bufio"
	"io/ioutil"
	"net/http"
	"net/url"
	"crypto/tls"
	"encoding/json"
	"log"
	"fmt"
	"time"
	"flag"
	"runtime"
	"path/filepath"
	"strings"
	"sort"
)


type Configuration struct {
	Version			int
	Repositories	[]RepositoryConfiguration
}

type RepositoryConfiguration struct {
	ID					string
	Directory			string
	ReadOnly			bool
	RescanIntervalS		int
}


// HTTP Authentication
var (
	target		string
	authUser	string
	authPass	string
	csrfToken	string
	csrfFile	string
	apiKey		string
)

// HTTP Debounce
var (
	debounceTimeout = 300*time.Millisecond
	dirVsFiles = 10
)

// Main
var (
	stop = make(chan int)
)

func init() {
	flag.StringVar(&target, "target", "localhost:8080", "Target")
	flag.StringVar(&authUser, "user", "", "Username")
	flag.StringVar(&authPass, "pass", "", "Password")
	flag.StringVar(&csrfFile, "csrf", "", "CSRF token file")
	flag.StringVar(&apiKey, "api", "", "API key")
	flag.Parse()
	if !strings.Contains(target, "://") { target = "http://" + target }	
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
}

func main() {

	testWebGuiPost()

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
	r, err := http.NewRequest("GET", target+"/rest/config", nil)
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
	tr := &http.Transport{ TLSClientConfig: &tls.Config{InsecureSkipVerify : true} }
	client := &http.Client{Transport: tr, Timeout: 5*time.Second}
	res, err := client.Do(r)
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
	expandedDirectory := expandTilde(directory)
	sw, err := NewSyncWatcher()
	if sw == nil || err != nil {
		log.Fatal(err)
	}
	defer sw.Close()
	err = sw.Watch(expandedDirectory)
	if err != nil {
		log.Fatal(err)
	}
	informChangeDebounced := informChangeDebounce(debounceTimeout, repo, directory, dirVsFiles, informChange)
	log.Println("Watching " + repo + ": " + directory)
	for {
		ev := waitForEvent(sw)
		if ev == nil {
			log.Fatal("fsnotify event is nil")
		}
		log.Println("Change detected in " + ev.Name)
		informChangeDebounced(ev.Name)
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

func testWebGuiPost() {
	r, err := http.NewRequest("POST", target+"/rest/404", nil)
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
	if res.StatusCode != 404 {
		log.Fatalf("Status %d != 404 for POST", res.StatusCode)
	}
}

func informChange(repo string, sub string) {
	data := url.Values {}
	data.Set("repo", repo)
	data.Set("sub", sub)
	r, err := http.NewRequest("POST", target+"/rest/scan?"+data.Encode(), nil)
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
	tr := &http.Transport{ TLSClientConfig: &tls.Config{InsecureSkipVerify : true} }
	client := &http.Client{Transport: tr, Timeout: 5*time.Second}
	res, err := client.Do(r)
	if err != nil {
		log.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		log.Fatalf("Status %d != 200 for POST", res.StatusCode)
	} else {
		log.Println("Syncthing is indexing change in " + repo + ": " + sub)
	}
}


func informChangeDebounce(interval time.Duration, repo string, repoDirectory string, dirVsFiles int, callback func(repo string, sub string)) func(string) {
	debounce := func(f func(paths []string)) func(string) {
		timer := &time.Timer{}
		subs := make([]string, 0)
		return func(sub string) {
			timer.Stop()
			subs = append(subs, sub)
			timer = time.AfterFunc(interval, func() {
				f(subs)
				subs = make([]string, 0)
			})
		}
	}
	
	return debounce(func(paths []string) {
		// Do not inform Syncthing immediately but wait for debounce
		// Therefore, we need to keep track of the paths that were changed
		// This function optimises tracking in two ways:
		//	- If there are more than `dirVsFiles` changes in a directory, we inform Syncthing to scan the entire directory
		//	- Directories with parent directory changes are aggregated. If A/B has 3 changes and A/C has 8, A will have 11 changes and if this is bigger than dirVsFiles we will scan A.
		if (len(paths) == 0) { return }
		trackedPaths := make(map[string]int) // Map directories to scores; if score == -1 the path is a filename
		sort.Strings(paths) // Make sure parent paths are processed first
		for i := range paths {
			path := paths[i]
			dir := filepath.Dir(path)
			score := 1 // File change counts for 1 per directory
			if dir == filepath.Clean(path) {
				score = dirVsFiles // Is directory itself, should definitely inform
			}
			// Search for existing parent directory relations in the map
			for trackedPath, _ := range trackedPaths {
				if strings.Contains(dir, trackedPath) {
					// Increment score of tracked current/parent directory
					trackedPaths[trackedPath] += score
				}
			}
			_, exists := trackedPaths[dir]
			if !exists {
				trackedPaths[dir] = score
			}
			trackedPaths[path] = -1
		}
		var keys []string
		for k := range trackedPaths {
			keys = append(keys, k)
		}
		sort.Strings(keys) // Sort directories before their own files
		previousDone, previousPath := false, ""
		for i := range keys {
			trackedPath := keys[i]
			trackedPathScore, _ := trackedPaths[trackedPath]
			if previousDone && strings.Contains(trackedPath, previousPath) { continue } // Already informed parent directory change
			if trackedPathScore < dirVsFiles && trackedPathScore != -1 { continue } // Not enough files for this directory or it is a file
			previousDone = trackedPathScore != -1
			previousPath = trackedPath
			sub := strings.TrimPrefix(trackedPath, repoDirectory)
			sub = strings.TrimPrefix(sub, string(os.PathSeparator))
			callback(repo, sub)
		}
	})
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
		log.Fatal("No home directory found - set $HOME (or the platform equivalent).")
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

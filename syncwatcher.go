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
	"regexp"
)


type Configuration struct {
	Version			int
	Folders			[]FolderConfiguration
}

type FolderConfiguration struct {
	ID			string
	Path			string
	ReadOnly		bool
	RescanIntervalS		int
}

type Pattern struct {
	match			*regexp.Regexp
	include			bool
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
	maxFiles = 5000
)

// Main
var (
	stop = make(chan int)
	ignorePaths = []string{".stversions", ".stfolder", ".stignore", ".syncthing", "~syncthing~"}
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

	folders := getFolders()
	if (len(folders) == 0) {
		log.Fatal("No folders found");
	}
	for i := range folders {
		go watchFolder(folders[i])
	}

	code := <-stop
	println("Exiting")
	os.Exit(code)

}

func getIgnorePatterns(folder string) []Pattern {
	r, err := http.NewRequest("GET", target+"/rest/ignores?folder="+folder, nil)
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
	var ignores map[string][]string
	err = json.Unmarshal(bs, &ignores)
	if err != nil {
		log.Fatal(err)
	}
	patterns := make([]Pattern, len(ignores["patterns"]))
	for i, str := range ignores["patterns"] {
		pattern := strings.TrimPrefix(str, "(?exclude)")
		println(pattern)
		regexp, err := regexp.Compile(pattern)
		if err != nil {
			log.Fatal(err)
		}
		patterns[i] = Pattern { regexp, str == pattern }
	}
	return patterns
}



func getFolders() []FolderConfiguration {
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
	return cfg.Folders
}

func watchFolder(folder FolderConfiguration) {
	path := expandTilde(folder.Path)
	ignorePatterns := getIgnorePatterns(folder.ID)
	sw, err := NewSyncWatcher(ignorePaths, ignorePatterns)
	if sw == nil || err != nil {
		log.Println(err)
		return
	}
	defer sw.Close()
	err = sw.Watch(path)
	if err != nil {
		log.Println(err)
		return
	}
	informChannel := make(chan string, 10)
	go informChangeAccumulator(debounceTimeout, folder.ID, path, dirVsFiles, informChannel, informChange)
	log.Println("Watching " + folder.ID + ": " + path)
	if folder.RescanIntervalS < 1800 {
		log.Printf("The rescan interval of folder %s can be increased to 3600 (an hour) or even 86400 (a day) as changes should be observed immediately while syncthing-inotify is running.", folder.ID)
	}
	for {
		ev := waitForEvent(sw)
		if ev == nil {
			log.Println("Error: fsnotify event is nil")
			continue
		}
		log.Println("Change detected in " + ev.Name)
		informChannel <- ev.Name
	}
}

func waitForEvent(sw *SyncWatcher) (ev *fsnotify.FileEvent) {
	var ok bool
	select {
		case ev, ok = <-sw.Event:
			if !ok {
				log.Println("Error: channel closed")
			}
		case err, eok := <-sw.Error:
			log.Println(err, eok)
	}
	return
}

func testWebGuiPost() (bool) {
	r, err := http.NewRequest("POST", target+"/rest/404", nil)
	if err != nil {
		log.Println("Cannot connect to Syncthing", err)
		return false
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
	r.Close = true
	tr := &http.Transport{ TLSClientConfig: &tls.Config{InsecureSkipVerify : true} }
	client := &http.Client{Transport: tr, Timeout: 5*time.Second}
	res, err := client.Do(r)
	if err != nil {
		log.Println("Cannot connect to Syncthing", err)
		return false
	}
	defer res.Body.Close()
	if res.StatusCode != 404 {
		log.Printf("Cannot connect to Syncthing, Status %d != 404 for POST\n", res.StatusCode)
		return false
	}
	return true
}

func informChange(folder string, sub string) (bool) {
	data := url.Values {}
	data.Set("folder", folder)
	data.Set("sub", sub)
	r, err := http.NewRequest("POST", target+"/rest/scan?"+data.Encode(), nil)
	if err != nil {
		log.Println(err)
		return false
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
	r.Close = true
	tr := &http.Transport{ TLSClientConfig: &tls.Config{InsecureSkipVerify : true} }
	client := &http.Client{Transport: tr, Timeout: 5*time.Second}
	res, err := client.Do(r)
	if err != nil {
		log.Println("Syncthing returned error", err)
		return false
	}
	if res.StatusCode != 200 {
		log.Printf("Error: Status %d != 200 for POST.\n" + folder + ": " + sub, res.StatusCode)
		return false
	} else {
		log.Println("Syncthing is indexing change in " + folder + ": " + sub)
	}
	return true
}

func informChangeAccumulator(interval time.Duration, folder string, folderPath string, dirVsFiles int, input chan string,
			callback func(folder string, sub string) (bool)) func(string) {
	debounce := func(f func(paths []string) (bool)) func(string) {
		subs := make([]string, 0)
		var item = <-input
		for {
			select {
				case item = <-input:
					if len(subs) < maxFiles {
						subs = append(subs, item)
					}
				case <-time.After(interval):
					ok := false
					if len(subs) < maxFiles {
						ok = f(subs)
					} else {
						ok = f([]string{ folderPath })
					}
					if ok {
						subs = make([]string, 0)
					}
			}
		}
	}
	
	return debounce(func(paths []string) (bool) {
		// Do not inform Syncthing immediately but wait for debounce
		// Therefore, we need to keep track of the paths that were changed
		// This function optimises tracking in two ways:
		//	- If there are more than `dirVsFiles` changes in a directory, we inform Syncthing to scan the entire directory
		//	- Directories with parent directory changes are aggregated. If A/B has 3 changes and A/C has 8, A will have 11 changes and if this is bigger than dirVsFiles we will scan A.
		if (len(paths) == 0) { return false }
		trackedPaths := make(map[string]int) // Map directories to scores; if score == -1 the path is a filename
		sort.Strings(paths) // Make sure parent paths are processed first
		previousPath := "" // Filter duplicates
		for i := range paths {
			path := paths[i]
			if (path == previousPath) {
				continue
			}
			previousPath = path
			dir := filepath.Dir(path)
			score := 1 // File change counts for 1 per directory
			if dir == filepath.Clean(path) {
				score = dirVsFiles // Is directory itself, should definitely inform
			}
			// Search for existing parent directory relations in the map
			for trackedPath, _ := range trackedPaths {
				if strings.HasPrefix(dir, trackedPath) {
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
			if previousDone && strings.HasPrefix(trackedPath, previousPath) { continue } // Already informed parent directory change
			if trackedPathScore < dirVsFiles && trackedPathScore != -1 { continue } // Not enough files for this directory or it is a file
			previousDone = trackedPathScore != -1
			previousPath = trackedPath
			sub := strings.TrimPrefix(trackedPath, folderPath)
			sub = strings.TrimPrefix(sub, string(os.PathSeparator))
			if !callback(folder, sub) {
				return false
			}
		}
		return true
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
		log.Fatal("No home path found - set $HOME (or the platform equivalent).")
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

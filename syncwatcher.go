// syncwatcher.go
package main

import (
	"bufio"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"github.com/cenkalti/backoff"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Configuration struct {
	Version int
	Folders []FolderConfiguration
}

type FolderConfiguration struct {
	ID              string
	Path            string
	ReadOnly        bool
	RescanIntervalS int
}

type Pattern struct {
	match   *regexp.Regexp
	include bool
}

type Event struct {
	ID   int         `json:"id"`
	Time time.Time   `json:"time"`
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

type STEvent struct {
	Path     string
	Finished bool
}

type STConfig struct {
	CsrfFile string
	ApiKey   string `xml:"gui>apikey"`
	Target   string `xml:"gui>address"`
	AuthUser string `xml:"gui.user"`
}

// HTTP Authentication
var (
	target    string
	authUser  string
	authPass  string
	csrfToken string
	csrfFile  string
	apiKey    string
)

// HTTP Timeouts
var (
	requestTimeout = 30 * time.Second
)

// HTTP Debounce
var (
	debounceTimeout    = 500 * time.Millisecond
	remoteIndexTimeout = 600 * time.Millisecond
	configSyncTimeout  = 5 * time.Second
	dirVsFiles         = 100
	maxFiles           = 5000
)

// Main
var (
	stop        = make(chan int)
	ignorePaths = []string{".stversions", ".stfolder", ".stignore", ".syncthing"}
	Discard     = log.New(ioutil.Discard, "", log.Ldate)
	Warning     = Discard // 1
	OK          = Discard
	Trace       = Discard
	Debug       = Discard // 4
)

func init() {
	c, _ := getSTConfig()

	var verbosity int
	flag.IntVar(&verbosity, "verbosity", 2, "Logging level [1..4]")
	flag.StringVar(&target, "target", c.Target, "Target")
	flag.StringVar(&authUser, "user", c.AuthUser, "Username")
	flag.StringVar(&authPass, "pass", "", "Password")
	flag.StringVar(&csrfFile, "csrf", "", "CSRF token file")
	flag.StringVar(&apiKey, "api", c.ApiKey, "API key")
	flag.Parse()

	if verbosity >= 1 {
		Warning = log.New(os.Stdout, "[WARNING] ", log.Ldate|log.Ltime|log.Lshortfile)
	}
	if verbosity >= 2 {
		OK = log.New(os.Stdout, "[OK] ", log.Ldate|log.Ltime|log.Lshortfile)
	}
	if verbosity >= 3 {
		Trace = log.New(os.Stdout, "[TRACE] ", log.Ldate|log.Ltime|log.Lshortfile)
	}
	if verbosity >= 4 {
		Debug = log.New(os.Stdout, "[DEBUG] ", log.Ldate|log.Ltime|log.Lshortfile)
	}
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	if len(csrfFile) > 0 {
		fd, err := os.Open(csrfFile)
		if err != nil {
			log.Fatalln(err)
		}
		s := bufio.NewScanner(fd)
		for s.Scan() {
			csrfToken = s.Text()
		}
		fd.Close()
	}
}

func main() {

	backoff.Retry(testWebGuiPost, backoff.NewExponentialBackOff())

	folders := getFolders()
	if len(folders) == 0 {
		log.Fatalln("No folders found")
	}
	stChans := make(map[string]chan STEvent, len(folders))
	for _, folder := range folders {
		stChan := make(chan STEvent)
		stChans[folder.ID] = stChan
		go watchFolder(folder, stChan)
	}
	go watchSTEvents(stChans, folders) // Note: Lose thread ownership of stChans

	code := <-stop
	OK.Println("Exiting")
	os.Exit(code)

}

func restart() bool {
	pgm, err := exec.LookPath(os.Args[0])
	if err != nil {
		Warning.Println("Cannot restart:", err)
		return false
	}
	env := os.Environ()
	newEnv := make([]string, 0, len(env))
	for _, s := range env {
		newEnv = append(newEnv, s)
	}
	proc, err := os.StartProcess(pgm, os.Args, &os.ProcAttr{
		Env:   newEnv,
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		Warning.Println("Cannot restart:", err)
		return false
	}
	proc.Release()
	stop <- 0
	return true
}

func getIgnorePatterns(folder string) []Pattern {
	for {
		Trace.Println("Getting Ignore Patterns: " + folder)
		r, err := http.NewRequest("GET", target+"/rest/ignores?folder="+folder, nil)
		res, err := performRequest(r)
		defer func() {
			if res != nil && res.Body != nil {
				res.Body.Close()
			}
		}()
		if err != nil {
			Warning.Println("Failed to perform request /rest/ignores: ", err)
			time.Sleep(configSyncTimeout)
			continue
		}
		if res.StatusCode == 500 {
			Warning.Println("Syncthing not ready in " + folder + " for /rest/ignores")
			time.Sleep(configSyncTimeout)
			continue
		}
		if res.StatusCode != 200 {
			log.Fatalf("Status %d != 200 for GET /rest/ignores: ", res.StatusCode, res)
		}
		bs, err := ioutil.ReadAll(res.Body)
		if err != nil {
			log.Fatalln(err)
		}
		var ignores map[string][]string
		err = json.Unmarshal(bs, &ignores)
		if err != nil {
			log.Fatalln(err)
		}
		patterns := make([]Pattern, len(ignores["patterns"]))
		for i, str := range ignores["patterns"] {
			pattern := strings.TrimPrefix(str, "(?exclude)")
			regexp, err := regexp.Compile(pattern)
			if err != nil {
				log.Fatalln(err)
			}
			patterns[i] = Pattern{regexp, str == pattern}
		}
		return patterns
	}
}

func getFolders() []FolderConfiguration {
	Trace.Println("Getting Folders")
	r, err := http.NewRequest("GET", target+"/rest/config", nil)
	res, err := performRequest(r)
	defer func() {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
	}()
	if err != nil {
		log.Fatalln("Failed to perform request /rest/config: ", err)
	}
	if res.StatusCode != 200 {
		log.Fatalf("Status %d != 200 for GET /rest/config: ", res.StatusCode)
	}
	bs, err := ioutil.ReadAll(res.Body)
	if err != nil {
		log.Fatalln(err)
	}
	var cfg Configuration
	err = json.Unmarshal(bs, &cfg)
	if err != nil {
		log.Fatalln(err)
	}
	return cfg.Folders
}

func watchFolder(folder FolderConfiguration, stInput chan STEvent) {
	folderPath := expandTilde(folder.Path)
	ignorePatterns := getIgnorePatterns(folder.ID)
	fsInput := make(chan string)
	sw, err := NewSyncWatcher(folderPath, ignorePaths, ignorePatterns)
	if sw == nil || err != nil {
		Warning.Println(err)
		return
	}
	defer sw.Close()
	err = sw.Watch(folderPath)
	if err != nil {
		Warning.Println(err)
		return
	}
	go accumulateChanges(debounceTimeout, folder.ID, folderPath, dirVsFiles, stInput, fsInput, informChange)
	OK.Println("Watching " + folder.ID + ": " + folderPath)
	if folder.RescanIntervalS < 1800 {
		OK.Printf("The rescan interval of folder %s can be increased to 3600 (an hour) or even 86400 (a day) as changes should be observed immediately while syncthing-inotify is running.", folder.ID)
	}
	for {
		evPath := waitForEvent(sw)
		Debug.Println("Change detected in: " + evPath + " (could still be ignored)")
		ev := relativePath(evPath, folderPath)
		if shouldIgnore(ignorePaths, ignorePatterns, ev) {
			continue
		}
		Trace.Println("Change detected in: " + evPath)
		fsInput <- ev
	}
}

func relativePath(path string, folderPath string) string {
	path = strings.TrimPrefix(path, folderPath)
	if len(path) == 0 {
		return path
	}
	if os.IsPathSeparator(path[0]) {
		path = path[1:len(path)]
	}
	return path
}

func waitForEvent(sw *SyncWatcher) string {
	select {
	case ev, ok := <-sw.Event:
		if !ok {
			Warning.Println("Error: channel closed")
		}
		return ev.Name
	case err, eok := <-sw.Error:
		Warning.Println(err, eok)
	}
	return ""
}

func shouldIgnore(ignorePaths []string, ignorePatterns []Pattern, path string) bool {
	if len(path) == 0 {
		return false
	}
	for _, ignorePath := range ignorePaths {
		if strings.Contains(path, ignorePath) {
			Debug.Println("Ignoring", path)
			return true
		}
	}
	for _, p1 := range ignorePatterns {
		if p1.include && p1.match.MatchString(path) {
			keep := false
			for _, p2 := range ignorePatterns {
				if !p2.include && p2.match.MatchString(path) {
					Debug.Println("Keeping", path, "because", p2.match.String())
					keep = true
					break
				}
			}
			if !keep {
				Debug.Println("Ignoring", path)
				return true
			}
		}
	}
	return false
}

func performRequest(r *http.Request) (*http.Response, error) {
	if r == nil {
		return nil, errors.New("Invalid HTTP Request object")
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
	tr := &http.Transport{
		TLSClientConfig:       &tls.Config{InsecureSkipVerify: true},
		ResponseHeaderTimeout: requestTimeout,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   requestTimeout,
	}
	res, err := client.Do(r)
	return res, err
}

func testWebGuiPost() error {
	Trace.Println("Testing WebGUI")
	r, err := http.NewRequest("POST", target+"/rest/404", nil)
	res, err := performRequest(r)
	defer func() {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
	}()
	if err != nil {
		Warning.Println("Cannot connect to Syncthing:", err)
		return err
	}
	if res.StatusCode != 404 {
		Warning.Printf("Cannot connect to Syncthing, Status %d != 404 for POST\n", res.StatusCode)
		return errors.New("Invalid HTTP status code")
	}
	return nil
}

func informChange(folder string, sub string) error {
	data := url.Values{}
	data.Set("folder", folder)
	data.Set("sub", sub)
	Trace.Println("Informing ST: " + folder + " :" + sub)
	r, _ := http.NewRequest("POST", target+"/rest/scan?"+data.Encode(), nil)
	res, err := performRequest(r)
	defer func() {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
	}()
	if err != nil {
		Warning.Println("Failed to perform request", err)
		return err
	}
	if res.StatusCode != 200 {
		Warning.Printf("Error: Status %d != 200 for POST.\n"+folder+": "+sub, res.StatusCode)
		return errors.New("Invalid HTTP status code")
	} else {
		OK.Println("Syncthing is indexing change in " + folder + ": " + sub)
	}
	// Wait until scan finishes
	_, err = ioutil.ReadAll(res.Body)
	return err
}

func accumulateChanges(interval time.Duration,
	folder string, folderPath string, dirVsFiles int,
	stInput chan STEvent, fsInput chan string,
	callback func(folder string, sub string) error) func(string) {
	inProgress := make(map[string]bool) // [Path string, InProgress bool]
	currInterval := interval
	for {
		select {
		case item := <-stInput:
			Debug.Println("STInput")
			if item.Path == "" {
				// Prepare for incoming changes
				currInterval = remoteIndexTimeout
				Debug.Println("Incoming Changes")
				continue
			}
			if item.Finished {
				// Ensure path is cleared when receiving itemFinished
				delete(inProgress, item.Path)
				Debug.Println("Remove Tracking ST: " + item.Path)
				continue
			}
			if len(inProgress) > maxFiles {
				continue
			}
			inProgress[item.Path] = true
		case item := <-fsInput:
			Debug.Println("FSInput")
			p, ok := inProgress[item]
			if p && ok {
				// Change originated from ST
				delete(inProgress, item)
				Debug.Println("Remove Tracking FS: " + item)
				continue
			}
			if len(inProgress) > maxFiles {
				continue
			}
			inProgress[item] = false
		case <-time.After(currInterval):
			currInterval = interval
			if len(inProgress) == 0 {
				continue
			}
			Debug.Println("Timeout AccumulateChanges")
			var err error
			var paths []string
			if len(inProgress) < maxFiles {
				paths = make([]string, len(inProgress))
				i := 0
				for path, progress := range inProgress {
					if !progress {
						paths[i] = path
						i++
					} else {
						Debug.Println("Waiting for: " + path)
					}
				}
				if len(paths) == 0 {
					Debug.Println("Empty paths")
					continue
				}
				// Try to inform changes to syncthing and if succeeded, clean up
				err = aggregateChanges(folder, folderPath, dirVsFiles, callback, paths)
			} else {
				// Do not track more than maxFiles changes, inform syncthing to rescan entire folder
				err = aggregateChanges(folder, folderPath, dirVsFiles, callback, []string{folderPath})
			}
			if err == nil {
				for _, path := range paths {
					delete(inProgress, path)
					Debug.Println("Remove Tracking Informed: " + path)
				}
			}
		}
	}
}

func aggregateChanges(folder string, folderPath string, dirVsFiles int, callback func(folder string, folderPath string) error, paths []string) error {
	// This function optimises tracking in two ways:
	//	- If there are more than `dirVsFiles` changes in a directory, we inform Syncthing to scan the entire directory
	//	- Directories with parent directory changes are aggregated. If A/B has 3 changes and A/C has 8, A will have 11 changes and if this is bigger than dirVsFiles we will scan A.
	if len(paths) == 0 {
		return errors.New("No changes to aggregate")
	}
	trackedPaths := make(map[string]int) // Map directories to scores; if score == -1 the path is a filename
	sort.Strings(paths)                  // Make sure parent paths are processed first
	previousPath := ""                   // Filter duplicates
	for i := range paths {
		path := paths[i]
		if path == previousPath {
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
		if previousDone && strings.HasPrefix(trackedPath, previousPath) {
			continue
		} // Already informed parent directory change
		if trackedPathScore < dirVsFiles && trackedPathScore != -1 {
			continue
		} // Not enough files for this directory or it is a file
		previousDone = trackedPathScore != -1
		previousPath = trackedPath
		sub := strings.TrimPrefix(trackedPath, folderPath)
		sub = strings.TrimPrefix(sub, string(os.PathSeparator))
		err := callback(folder, sub)
		if err != nil {
			return err
		}
	}
	return nil
}

func watchSTEvents(stChans map[string]chan STEvent, folders []FolderConfiguration) {
	lastSeenID := 0
	for {
		events, err := getSTEvents(lastSeenID)
		if err != nil {
			// Probably Syncthing restarted
			lastSeenID = 0
			time.Sleep(configSyncTimeout)
			continue
		}
		if events == nil {
			continue
		}
		for _, event := range events {
			switch event.Type {
			case "RemoteIndexUpdated":
				data := event.Data.(map[string]interface{})
				ch, ok := stChans[data["folder"].(string)]
				if !ok {
					continue
				}
				ch <- STEvent{Path: "", Finished: false}
			case "ItemStarted":
				data := event.Data.(map[string]interface{})
				ch, ok := stChans[data["folder"].(string)]
				if !ok {
					continue
				}
				ch <- STEvent{Path: data["item"].(string), Finished: false}
			case "ItemFinished":
				data := event.Data.(map[string]interface{})
				ch, ok := stChans[data["folder"].(string)]
				if !ok {
					continue
				}
				ch <- STEvent{Path: data["item"].(string), Finished: true}
			case "ConfigSaved":
				Trace.Println("ConfigSaved, exiting if folders changed")
				go waitForSyncAndExitIfNeeded(folders)
			}
		}
		lastSeenID = events[len(events)-1].ID
	}
}

func getSTEvents(lastSeenID int) ([]Event, error) {
	Trace.Println("Requesting STEvents: " + strconv.Itoa(lastSeenID))
	r, err := http.NewRequest("GET", target+"/rest/events?since="+strconv.Itoa(lastSeenID), nil)
	res, err := performRequest(r)
	defer func() {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
	}()
	if err != nil {
		Warning.Println("Failed to perform request", err)
		return nil, err
	}
	if res.StatusCode != 200 {
		Warning.Printf("Status %d != 200 for GET", res.StatusCode)
		return nil, errors.New("Invalid HTTP status code")
	}
	bs, err := ioutil.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	var events []Event
	err = json.Unmarshal(bs, &events)
	return events, err
}

func waitForSyncAndExitIfNeeded(folders []FolderConfiguration) {
	waitForSync()
	newFolders := getFolders()
	same := len(folders) == len(newFolders)
	for _, newF := range newFolders {
		seen := false
		for _, f := range folders {
			if f.ID == newF.ID && f.Path == newF.Path {
				seen = true
			}
		}
		if !seen {
			Warning.Println("Folder " + newF.ID + " changed")
			same = false
		}
	}
	if !same {
		// Simply exit as folders:
		// - can be added (still ok)
		// - can be removed as well (requires informing tons of goroutines...)
		OK.Println("Syncthing folder configuration updated, restarting")
		if !restart() {
			log.Fatalln("Cannot restart syncthing-inotify, exiting")
		}
	}
}

func waitForSync() {
	for {
		Trace.Println("Waiting for Sync")
		r, err := http.NewRequest("GET", target+"/rest/config/sync", nil)
		res, err := performRequest(r)
		defer func() {
			if res != nil && res.Body != nil {
				res.Body.Close()
			}
		}()
		if err != nil {
			Warning.Println("Failed to perform request /rest/config/sync", err)
			time.Sleep(configSyncTimeout)
			continue
		}
		if res.StatusCode != 200 {
			Warning.Printf("Status %d != 200 for GET", res.StatusCode)
			time.Sleep(configSyncTimeout)
			continue
		}
		bs, err := ioutil.ReadAll(res.Body)
		if err != nil {
			time.Sleep(configSyncTimeout)
			continue
		}
		var inSync map[string]bool
		err = json.Unmarshal(bs, &inSync)
		if inSync["configInSync"] {
			return
		}
		time.Sleep(configSyncTimeout)
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
		log.Fatalln("No home path found - set $HOME (or the platform equivalent).")
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

func getSTConfig() (STConfig, error) {
	var path = filepath.Join(getSTDefaultConfDir(), "config.xml")
	c := STConfig{Target: "localhost:8080"}

	if file, err := os.Open(path); err != nil {
		return c, err
	} else {
		err := xml.NewDecoder(file).Decode(&c)
		if err != nil {
			return c, err
		}
	}

	// This is not in the XML, but we can determine a sane default
	c.CsrfFile = filepath.Join(getSTDefaultConfDir(), "csrftokens.txt")

	return c, nil
}

// inspired by syncthing/cmd/syncthing/main.go#L941
func getSTDefaultConfDir() string {
	switch runtime.GOOS {
	case "windows":
		return filepath.Join(os.Getenv("LocalAppData"), "Syncthing")

	case "darwin":
		return expandTilde("~/Library/Application Support/Syncthing")

	default:
		if xdgCfg := os.Getenv("XDG_CONFIG_HOME"); xdgCfg != "" {
			return filepath.Join(xdgCfg, "syncthing")
		}
		return expandTilde("~/.config/syncthing")
	}
}

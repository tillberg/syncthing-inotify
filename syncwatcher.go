// syncwatcher.go
package main

import (
	"bufio"
	"bytes"
	"crypto/tls"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
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
	"text/tabwriter"
	"time"

	"github.com/cenkalti/backoff"
	"github.com/zillode/notify"
)

// Configuration is used in parsing response from ST
type Configuration struct {
	Version int
	Folders []FolderConfiguration
}

// FolderConfiguration holds information about shared folder in ST
type FolderConfiguration struct {
	ID              string
	Path            string
	ReadOnly        bool
	RescanIntervalS int
}

// Pattern holds ignored path and a boolean which value is false when we should use the pattern in exclude mode
type Pattern struct {
	match   *regexp.Regexp
	include bool
}

// Event holds full event data coming from Syncthing REST API
type Event struct {
	ID   int         `json:"id"`
	Time time.Time   `json:"time"`
	Type string      `json:"type"`
	Data interface{} `json:"data"`
}

// STEvent holds simplified data for Syncthing event. Path can be empty in the case of event.type="RemoteIndexUpdated"
type STEvent struct {
	Path     string
	Finished bool
}

// STNestedConfig is used for unpacking config from XML format
type STNestedConfig struct {
	Config STConfig `xml:"gui"`
}

// STConfig is used for unpacking gui part of config from XML format
type STConfig struct {
	CsrfFile string
	APIKey   string `xml:"apikey"`
	Target   string `xml:"address"`
	AuthUser string `xml:"user"`
	AuthPass string `xml:"password"`
	TLS      bool   `xml:"tls,attr"`
}

type folderSlice []string

type progressTime struct {
	fsEvent bool // true - event was triggered by filesystem, false - by Syncthing
	time    time.Time
}

func (fs *folderSlice) String() string {
	return fmt.Sprint(*fs)
}
func (fs *folderSlice) Set(value string) error {
	for _, f := range strings.Split(value, ",") {
		*fs = append(*fs, f)
	}
	return nil
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
	requestTimeout = 180 * time.Second
)

// HTTP Debounce
var (
	debounceTimeout   = 500 * time.Millisecond
	configSyncTimeout = 5 * time.Second
	fsEventTimeout    = 5 * time.Second
	dirVsFiles        = 128
	maxFiles          = 512
)

// Main
var (
	stop         = make(chan int)
	ignorePaths  = []string{".stversions", ".syncthing.", "~syncthing~"}
	Version      = "unknown-dev"
	Discard      = log.New(ioutil.Discard, "", log.Ldate)
	Warning      = Discard // verbosity=1
	OK           = Discard // 2
	Trace        = Discard // 3
	Debug        = Discard // 4
	watchFolders folderSlice
	skipFolders  folderSlice
	delayScan    = 3600
)

const (
	pathSeparator = string(os.PathSeparator)
	usage         = "syncthing-inotify [options]"
	extraUsage    = `
The -logflags value is a sum of the following:

   1  Date
   2  Time
   4  Microsecond time
   8  Long filename
  16  Short filename

I.e. to prefix each log line with date and time, set -logflags=3 (1 + 2 from
above). The value 0 is used to disable all of the above. The default is to
show time only (2).`
)

func init() {
	c, _ := getSTConfig(getSTDefaultConfDir())
	if !strings.Contains(c.Target, "://") {
		if c.TLS {
			target = "https://" + c.Target
		} else {
			target = "http://" + c.Target
		}
	}

	var verbosity int
	var logflags int
	var home string
	var apiKeyStdin bool
	var authPassStdin bool
	var showVersion bool
	flag.IntVar(&verbosity, "verbosity", 2, "Logging level [1..4]")
	flag.IntVar(&logflags, "logflags", 2, "Select information in log line prefix")
	flag.StringVar(&home, "home", home, "Specify the home Syncthing dir to sniff configuration settings")
	flag.StringVar(&target, "target", target, "Target url (prepend with https:// for TLS)")
	flag.StringVar(&authUser, "user", c.AuthUser, "Username")
	flag.StringVar(&authPass, "password", "***", "Password")
	flag.StringVar(&csrfFile, "csrf", "", "CSRF token file")
	flag.StringVar(&apiKey, "api", c.APIKey, "API key")
	flag.BoolVar(&apiKeyStdin, "api-stdin", false, "Provide API key through stdin")
	flag.BoolVar(&authPassStdin, "password-stdin", false, "Provide password through stdin")
	flag.Var(&watchFolders, "folders", "A comma-separated list of folders to watch (all by default)")
	flag.Var(&skipFolders, "skip-folders", "A comma-separated list of folders to skip inotify watching")
	flag.IntVar(&delayScan, "delay-scan", delayScan, "Automatically delay next scan interval (in seconds)")
	flag.BoolVar(&showVersion, "version", false, "Show version")

	flag.Usage = usageFor(flag.CommandLine, usage, fmt.Sprintf(extraUsage))
	flag.Parse()

	if showVersion {
		fmt.Printf("syncthing-inotify %s (%s %s-%s)\n", Version, runtime.Version(), runtime.GOOS, runtime.GOARCH)
		os.Exit(0)
	}

	if verbosity >= 1 {
		Warning = log.New(os.Stdout, "[WARNING] ", logflags)
	}
	if verbosity >= 2 {
		OK = log.New(os.Stdout, "[OK] ", logflags)
	}
	if verbosity >= 3 {
		Trace = log.New(os.Stdout, "[TRACE] ", logflags)
	}
	if verbosity >= 4 {
		Debug = log.New(os.Stdout, "[DEBUG] ", logflags)
	}

	if len(home) > 0 {
		c, err := getSTConfig(home)
		if err != nil {
			log.Fatalln(err)
		}
		if !strings.Contains(c.Target, "://") {
			if c.TLS {
				target = "https://" + c.Target
			} else {
				target = "http://" + c.Target
			}
			apiKey = c.APIKey
		}
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
	if apiKeyStdin && authPassStdin {
		log.Fatalln("Either provide an API or password through stdin")
	}
	if apiKeyStdin {
		stdin := bufio.NewReader(os.Stdin)
		apiKey, _ = stdin.ReadString('\n')
	}
	if authPassStdin {
		stdin := bufio.NewReader(os.Stdin)
		authPass, _ = stdin.ReadString('\n')
	}
	if len(watchFolders) != 0 && len(skipFolders) != 0 {
		log.Fatalln("Either provide a list of folders to be watched or to be ignored, not both.")
	}
	if delayScan > 0 && delayScan < 60 {
		log.Fatalln("A delay scan interval shorter than 60 is not supported.")
	}
}

// main reads configs, starts all gouroutines and waits until a message is in channel stop.
func main() {
	backoff.Retry(testWebGuiPost, backoff.NewExponentialBackOff())

	allFolders := getFolders()
	folders := filterFolders(allFolders)
	if len(folders) == 0 {
		log.Fatalln("No folders to be watched, exiting...")
	}
	stChans := make(map[string]chan STEvent, len(folders))
	for _, folder := range folders {
		Debug.Println("Installing watch for " + folder.ID)
		stChan := make(chan STEvent)
		stChans[folder.ID] = stChan
		go watchFolder(folder, stChan)
	}
	// Note: Lose thread ownership of stChans
	go watchSTEvents(stChans, allFolders)

	code := <-stop
	OK.Println("Exiting")
	os.Exit(code)
}

// Restart uses path to itself and copy of environment to start new process.
// Then it sends message to stop channel to shutdown itself.
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
	stop <- 3
	return true
}

// filterFolders refines folders list using global vars watchFolders and skipFolders
func filterFolders(folders []FolderConfiguration) []FolderConfiguration {
	if len(watchFolders) > 0 {
		var fs []FolderConfiguration
		for _, f := range folders {
			for _, watch := range watchFolders {
				if f.ID == watch {
					fs = append(fs, f)
					break
				}
			}
		}
		return fs
	}
	if len(skipFolders) > 0 {
		var fs []FolderConfiguration
		for _, f := range folders {
			keep := true
			for _, skip := range skipFolders {
				if f.ID == skip {
					keep = false
					break
				}
			}
			if keep {
				fs = append(fs, f)
			}
		}
		return fs
	}
	return folders
}

// getIgnorePatterns retrieves the list of ignored patterns for a folder from Syncthing.
// It blocks until ST responds with success.
func getIgnorePatterns(folder string) []Pattern {
	for {
		Trace.Println("Getting ignore patterns for " + folder)
		r, err := http.NewRequest("GET", target+"/rest/db/ignores?folder="+url.QueryEscape(folder), nil)
		res, err := performRequest(r)
		defer func() {
			if res != nil && res.Body != nil {
				res.Body.Close()
			}
		}()
		if err != nil {
			Warning.Println("Failed to perform request /rest/db/ignores?folder="+url.QueryEscape(folder), err)
			time.Sleep(configSyncTimeout)
			continue
		}
		if res.StatusCode == 500 {
			Warning.Println("Syncthing not ready in " + folder + " for /rest/db/ignores")
			time.Sleep(configSyncTimeout)
			continue
		}
		if res.StatusCode != 200 {
			log.Fatalf("Status %d != 200 for GET /rest/db/ignores?folder=%s: %v\n", res.StatusCode, folder, res)
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

// getFolders returns the list of folders configured in Syncthing. Blocks until ST responded successfully.
func getFolders() []FolderConfiguration {
	Trace.Println("Getting Folders")
	r, err := http.NewRequest("GET", target+"/rest/system/config", nil)
	res, err := performRequest(r)
	defer func() {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
	}()
	if err != nil {
		log.Fatalln("Failed to perform request /rest/system/config: ", err)
	}
	if res.StatusCode != 200 {
		log.Fatalf("Status %d != 200 for GET /rest/system/config: ", res.StatusCode)
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

// watchFolder installs inotify watcher for a folder, launches
// goroutine which receives changed items. It never exits.
func watchFolder(folder FolderConfiguration, stInput chan STEvent) {
	folderPath := expandTilde(folder.Path)
	ignorePatterns := getIgnorePatterns(folder.ID)
	fsInput := make(chan string)
	c := make(chan notify.EventInfo, maxFiles)
	if err := notify.Watch(filepath.Join(folderPath, "..."), c, notify.All); err != nil {
		Warning.Println("Failed to install inotify handlers", err)
		informError("Failed to install inotify handler for " + folder.ID + ": " + err.Error())
		return
	}
	defer notify.Stop(c)
	go accumulateChanges(debounceTimeout, folder.ID, folderPath, dirVsFiles, stInput, fsInput, informChange)
	OK.Println("Watching " + folder.ID + ": " + folderPath)
	if folder.RescanIntervalS < 1800 && delayScan <= 0 {
		OK.Printf("The rescan interval of folder %s can be increased to 3600 (an hour) or even 86400 (a day) as changes should be observed immediately while syncthing-inotify is running.", folder.ID)
	}
	// will we ever get out of this loop?
	for {
		evPath := waitForEvent(c)
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

// waitForEvent waits for an event in a channel c and returns event.Path().
// When channel c is closed then it returns path for default event (not sure if this is used at all?)
func waitForEvent(c chan notify.EventInfo) string {
	select {
	case ev, ok := <-c:
		if !ok {
			// this is never reached b/c c is never closed
			Warning.Println("Error: channel closed")
		}
		return ev.Path()
	}
}

// shouldIgnore determines if path should be ignored using ignorePaths and ignorePatterns
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

// performRequest performs preparations to make an HTTP request r to Synthing API
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
		DisableKeepAlives:     true,
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   requestTimeout,
	}
	res, err := client.Do(r)
	return res, err
}

// testWebGuiPost tries to connect to Syncthing returning nil on success
func testWebGuiPost() error {
	Trace.Println("Testing WebGUI")
	r, err := http.NewRequest("GET", target+"/rest/404", nil)
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
	body, _ := ioutil.ReadAll(res.Body)
	if res.StatusCode != 404 {
		Warning.Printf("Cannot connect to Syncthing, Status %d != 404 for GET. Body: %v\n", res.StatusCode, string(body))
		return errors.New("Invalid HTTP status code")
	}
	return nil
}

// informError sends a msg error to Syncthing
func informError(msg string) error {
	Trace.Printf("Informing ST about inotify error: %v", msg)
	r, _ := http.NewRequest("POST", target+"/rest/system/error", strings.NewReader("[Inotify] "+msg))
	r.Header.Set("Content-Type", "plain/text")
	res, err := performRequest(r)
	defer func() {
		if res != nil && res.Body != nil {
			res.Body.Close()
		}
	}()
	if err != nil {
		Warning.Println("Failed to inform Syncthing about", msg, err)
		return err
	}
	if res.StatusCode == 403 {
		Warning.Printf("Error: HTTP POST forbidden. Missing API key?")
		return errors.New("HTTP POST forbidden")
	}
	if res.StatusCode != 200 {
		Warning.Printf("Error: Status %d != 200 for POST: %v\n", res.StatusCode, msg)
		return errors.New("Invalid HTTP status code")
	}
	return err
}

// informChange sends a request to rescan folder and subs to Syncthing
func informChange(folder string, subs []string) error {
	data := url.Values{}
	data.Set("folder", folder)
	for _, sub := range subs {
		data.Add("sub", sub)
	}
	if delayScan > 0 {
		data.Set("next", strconv.Itoa(delayScan))
	}
	Trace.Printf("Informing ST: %v: %v", folder, subs)
	r, _ := http.NewRequest("POST", target+"/rest/db/scan?"+data.Encode(), nil)
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
	if res.StatusCode == 403 {
		Warning.Printf("Error: HTTP POST forbidden. missing API key?")
		return errors.New("HTTP POST forbidden")
	}
	if res.StatusCode != 200 {
		msg, _ := ioutil.ReadAll(res.Body)
		Warning.Println(target + "/rest/db/scan?" + data.Encode())
		Warning.Printf("Error: Status %d != 200 for POST: %v, %s\n", res.StatusCode, folder, msg)
		return errors.New("Invalid HTTP status code")
	}
	OK.Printf("Syncthing is indexing change in %v: %v", folder, subs)

	// Wait until scan finishes
	_, err = ioutil.ReadAll(res.Body)
	return err
}

// InformCallback is a function which will be called from accumulateChanges when there is a change we need to inform Syncthing about
type InformCallback func(folder string, subs []string) error

// accumulateChanges filters out events that originate from ST.
// - it aggregates changes based on hierarchy structure
// - no redundant folder searches (abc + abc/d is useless)
// - no excessive large scans (abc/{1..1000} should become a scan of just abc folder)
// One of the difficulties is that we cannot know if deleted files were a directory or a file.
func accumulateChanges(debounceTimeout time.Duration,
	folder string,
	folderPath string,
	dirVsFiles int,
	stInput chan STEvent,
	fsInput chan string,
	callback InformCallback) func(string) {
	delayScanInterval := time.Duration(delayScan-5) * time.Second
	Debug.Printf("Delay scan reminder interval for %s set to %.0f seconds\n", folder, delayScanInterval.Seconds())
	inProgress := make(map[string]progressTime)       // [path string]{fs, start}
	currInterval := delayScanInterval                 // Timeout of the timer
	callback(folder, []string{".stfolder"})           // Inform Syncthing to delay scan interval
	nextScanTime := time.Now().Add(delayScanInterval) // Time to remind Syncthing to delay scan
	for {
		select {
		case item := <-stInput:
			if item.Path == "" {
				// Prepare for incoming changes
				currInterval = debounceTimeout
				Debug.Println("[ST] Incoming Changes for " + folder + ", speeding up inotify timeout parameters")
				continue
			}
			if item.Finished {
				// Ensure path is cleared when receiving itemFinished
				delete(inProgress, item.Path)
				Debug.Println("[ST] Removed tracking for " + item.Path)
				continue
			}
			if len(inProgress) > maxFiles {
				Debug.Println("[ST] Tracking too many files, aggregating STEvent: " + item.Path)
				continue
			}
			Debug.Println("[ST] Incoming: " + item.Path)
			inProgress[item.Path] = progressTime{false, time.Now()}
		case item := <-fsInput:
			currInterval = debounceTimeout
			Debug.Println("[FS] Incoming Changes for " + folder + ", speeding up inotify timeout parameters")
			p, ok := inProgress[item]
			if ok && !p.fsEvent {
				// Change originated from ST
				delete(inProgress, item)
				Debug.Println("[FS] Removed tracking for " + item)
				continue
			}
			if len(inProgress) > maxFiles {
				Debug.Println("[FS] Tracking too many files, aggregating FSEvent: " + item)
				continue
			}
			Debug.Println("[FS] Tracking: " + item)
			inProgress[item] = progressTime{true, time.Now()}
		case <-time.After(currInterval):
			if delayScan > 0 && nextScanTime.Before(time.Now()) {
				nextScanTime = time.Now().Add(delayScanInterval)
				Debug.Println("Periodically extend the nextScan interval for " + folder)
				callback(folder, []string{".stfolder"})
			}
			if len(inProgress) == 0 {
				if currInterval != delayScanInterval {
					Debug.Println("Slowing down inotify timeout parameters for " + folder)
					currInterval = delayScanInterval
				}
				continue
			}
			Debug.Println("Timeout AccumulateChanges")
			var err error
			var paths []string
			expiry := time.Now().Add(-debounceTimeout * 10)
			if len(inProgress) < maxFiles {
				for path, progress := range inProgress {
					// Clean up invalid and expired paths
					if path == "" || progress.time.Before(expiry) {
						delete(inProgress, path)
						continue
					}
					if progress.fsEvent {
						paths = append(paths, path)
						Debug.Println("Informing about " + path)
					} else {
						Debug.Println("Waiting for " + path)
					}
				}
				if len(paths) == 0 {
					Debug.Println("Empty paths")
					continue
				}

				// Try to inform changes to syncthing and if succeeded, clean up
				err = aggregateChanges(folder, folderPath, dirVsFiles, callback, paths)
				if err == nil {
					for _, path := range paths {
						delete(inProgress, path)
						Debug.Println("[INFORMED] Removed tracking for " + path)
					}
				}
			} else {
				// Do not track more than maxFiles changes, inform syncthing to rescan entire folder
				err = callback(folder, []string{""})
				if err == nil {
					for path, progress := range inProgress {
						if progress.fsEvent {
							delete(inProgress, path)
							Debug.Println("[INFORMED] Removed tracking for " + path)
						}
					}
				}
			}

			if err == nil {
				nextScanTime = time.Now().Add(delayScanInterval) // Scan was delayed
			} else {
				Warning.Println("Syncthing failed to index changes for ", folder, err)
				time.Sleep(configSyncTimeout)
			}
		}
	}
}

// AggregateChanges optimises tracking in two ways:
// - If there are more than `dirVsFiles` changes in a directory, we inform Syncthing to scan the entire directory
// - Directories with parent directory changes are aggregated. If A/B has 3 changes and A/C has 8, A will have 11 changes and if this is bigger than dirVsFiles we will scan A.
func aggregateChanges(folder string, folderPath string, dirVsFiles int, callback InformCallback, paths []string) error {
	if len(paths) == 0 {
		return errors.New("No changes to aggregate")
	}
	// Map paths to scores; if score == -1 the path is a filename
	trackedPaths := make(map[string]int)
	// Map of directories
	trackedDirs := make(map[string]bool)
	// Make sure parent paths are processed first
	sort.Strings(paths)
	// For removing duplicates in a sorted list
	previousPath := ""
	// First we collect all paths and calculate scores for them
	for i := range paths {
		path := filepath.Clean(paths[i])
		if path == "." {
			path = ""
		}
		if path == previousPath {
			continue
		}
		previousPath = path
		fi, _ := os.Stat(path)
		path = strings.TrimPrefix(path, folderPath)
		path = strings.TrimPrefix(path, pathSeparator)
		var dir string
		if fi == nil {
			// Definitely inform if the path does not exist anymore
			dir = path
			trackedPaths[path] = dirVsFiles
			Debug.Println("[AG] Not found:", path)
		} else if fi.IsDir() {
			// Definitely inform if a directory changed
			dir = path
			trackedPaths[path] = dirVsFiles
			trackedDirs[dir] = true
			Debug.Println("[AG] Is a dir:", dir)
		} else {
			Debug.Println("[AG] Is file:", path)
			// Files are linked to -1 scores
			// Also increment the parent path with 1
			dir = filepath.Dir(path)
			if dir == "." {
				dir = ""
			}
			trackedPaths[path] = -1
			trackedPaths[dir]++
			trackedDirs[dir] = true
		}
		// Search for existing parent directory relations in the map
		for trackedPath := range trackedPaths {
			if trackedDirs[trackedPath] && strings.HasPrefix(dir, trackedPath+pathSeparator) {
				// Increment score of tracked parent directory for each file
				trackedPaths[trackedPath]++
				Debug.Println("[AG] Increment:", trackedPath, trackedPaths, trackedPaths[trackedPath])
			}
		}
	}
	var keys []string
	for k := range trackedPaths {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Sort directories before their own files
	previousPath = ""
	var scans []string
	// Decide if we should inform about particular path based on dirVsFiles
	for i := range keys {
		trackedPath := keys[i]
		trackedPathScore, _ := trackedPaths[trackedPath]
		if strings.HasPrefix(trackedPath, previousPath+pathSeparator) {
			// Already informed parent directory change
			continue
		}
		if trackedPathScore < dirVsFiles && trackedPathScore != -1 {
			// Not enough files for this directory or it is a file
			continue
		}
		previousPath = trackedPath
		Debug.Println("[AG] Appending path:", trackedPath, previousPath)
		scans = append(scans, trackedPath)
		if trackedPath == "" {
			// If we need to scan everything, skip the rest
			break
		}
	}
	return callback(folder, scans)
}

// watchSTEvents reads events from Syncthing. For events of type ItemStarted and ItemFinished it puts
// them into aproppriate stChans, where key is a folder from event.
// For ConfigSaved event it spawns goroutine waitForSyncAndExitIfNeeded.
func watchSTEvents(stChans map[string]chan STEvent, folders []FolderConfiguration) {
	lastSeenID := 0
	for {
		events, err := getSTEvents(lastSeenID)
		if err != nil {
			// Work-around for Go <1.5 (https://github.com/golang/go/issues/9405)
			if strings.Contains(err.Error(), "use of closed network connection") {
				continue
			}

			// Syncthing probably restarted
			Debug.Println("Resetting STEvents", err)
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

// getSTEvents returns a list of events which happened in Syncthing since lastSeenID.
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

// waitForSyncAndExitIfNeeded performs restart of itself if folders has different configuration in syncthing.
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

// waitForSync blocks execution until syncthing is in sync
func waitForSync() {
	for {
		Trace.Println("Waiting for Sync")
		r, err := http.NewRequest("GET", target+"/rest/system/config/insync", nil)
		res, err := performRequest(r)
		defer func() {
			if res != nil && res.Body != nil {
				res.Body.Close()
			}
		}()
		if err != nil {
			Warning.Println("Failed to perform request /rest/system/config/insync", err)
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

func optionTable(w io.Writer, rows [][]string) {
	tw := tabwriter.NewWriter(w, 2, 4, 2, ' ', 0)
	for _, row := range rows {
		for i, cell := range row {
			if i > 0 {
				tw.Write([]byte("\t"))
			}
			tw.Write([]byte(cell))
		}
		tw.Write([]byte("\n"))
	}
	tw.Flush()
}

func usageFor(fs *flag.FlagSet, usage string, extra string) func() {
	return func() {
		var b bytes.Buffer
		b.WriteString("Usage:\n  " + usage + "\n")

		var options [][]string
		fs.VisitAll(func(f *flag.Flag) {
			var opt = "  -" + f.Name

			if f.DefValue == "[]" {
				f.DefValue = ""
			}
			if f.DefValue != "false" {
				opt += "=" + fmt.Sprintf(`"%s"`, f.DefValue)
			}
			options = append(options, []string{opt, f.Usage})
		})

		if len(options) > 0 {
			b.WriteString("\nOptions:\n")
			optionTable(&b, options)
		}

		fmt.Println(b.String())

		if len(extra) > 0 {
			fmt.Println(extra)
		}
	}
}

func getSTConfig(dir string) (STConfig, error) {
	var path = filepath.Join(dir, "config.xml")
	nc := STNestedConfig{Config: STConfig{Target: "localhost:8384"}}
	if file, err := os.Open(path); err != nil {
		return nc.Config, err
	} else {
		err := xml.NewDecoder(file).Decode(&nc)
		if err != nil {
			log.Fatal(err)
			return nc.Config, err
		}
	}
	// This is not in the XML, but we can determine a sane default
	nc.Config.CsrfFile = filepath.Join(getSTDefaultConfDir(), "csrftokens.txt")
	return nc.Config, nil
}

// inspired by https://github.com/syncthing/syncthing/blob/03bbf273b3614d97a4c642e466e8c5bfb39ef595/cmd/syncthing/main.go#L943
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

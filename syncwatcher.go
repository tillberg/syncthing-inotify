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
	"github.com/rjeczalik/notify"
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

type STNestedConfig struct {
	Config STConfig `xml:"gui"`
}

type STConfig struct {
	CsrfFile string
	ApiKey   string `xml:"apikey"`
	Target   string `xml:"address"`
	AuthUser string `xml:"user"`
	AuthPass string `xml:"password"`
	TLS      bool   `xml:"tls,attr"`
}

type folderSlice []string

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
	requestTimeout = 30 * time.Second
)

// HTTP Debounce
var (
	debounceTimeout    = 500 * time.Millisecond
	remoteIndexTimeout = 600 * time.Millisecond
	configSyncTimeout  = 5 * time.Second
	dirVsFiles         = 256
	maxFiles           = 5000
)

// Main
var (
	stop         = make(chan int)
	ignorePaths  = []string{".stversions", ".stfolder", ".stignore", ".syncthing"}
	Discard      = log.New(ioutil.Discard, "", log.Ldate)
	Warning      = Discard // 1
	OK           = Discard
	Trace        = Discard
	Debug        = Discard // 4
	watchFolders folderSlice
	skipFolders  folderSlice
)

const (
	usage      = "syncthing-inotify [options]"
	extraUsage = `
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
	c, _ := getSTConfig()
	if !strings.Contains(c.Target, "://") {
		if c.TLS {
			target = "https://" + c.Target
		} else {
			target = "http://" + c.Target
		}
	}

	var verbosity int
	var logflags int
	var apiKeyStdin bool
	var authPassStdin bool
	flag.IntVar(&verbosity, "verbosity", 2, "Logging level [1..4]")
	flag.IntVar(&logflags, "logflags", 2, "Select information in log line prefix")
	flag.StringVar(&target, "target", target, "Target url (prepend with https:// for TLS)")
	flag.StringVar(&authUser, "user", c.AuthUser, "Username")
	flag.StringVar(&authPass, "password", "***", "Password")
	flag.StringVar(&csrfFile, "csrf", "", "CSRF token file")
	flag.StringVar(&apiKey, "api", c.ApiKey, "API key")
	flag.BoolVar(&apiKeyStdin, "api-stdin", false, "Provide API key through stdin")
	flag.BoolVar(&authPassStdin, "password-stdin", false, "Provide password through stdin")
	flag.Var(&watchFolders, "folders", "A comma-separated list of folders to watch (all by default)")
	flag.Var(&skipFolders, "skip-folders", "A comma-separated list of folders to skip inotify watching")

	flag.Usage = usageFor(flag.CommandLine, usage, fmt.Sprintf(extraUsage))
	flag.Parse()

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
}

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
	go watchSTEvents(stChans, allFolders) // Note: Lose thread ownership of stChans

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
				break
			}
		}
		return fs
	}
	return folders
}

func getIgnorePatterns(folder string) []Pattern {
	for {
		Trace.Println("Getting Ignore Patterns: " + folder)
		r, err := http.NewRequest("GET", target+"/rest/db/ignores?folder="+folder, nil)
		res, err := performRequest(r)
		defer func() {
			if res != nil && res.Body != nil {
				res.Body.Close()
			}
		}()
		if err != nil {
			Warning.Println("Failed to perform request /rest/db/ignores: ", err)
			time.Sleep(configSyncTimeout)
			continue
		}
		if res.StatusCode == 500 {
			Warning.Println("Syncthing not ready in " + folder + " for /rest/db/ignores")
			time.Sleep(configSyncTimeout)
			continue
		}
		if res.StatusCode != 200 {
			log.Fatalf("Status %d != 200 for GET /rest/db/ignores: ", res.StatusCode, res)
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

func watchFolder(folder FolderConfiguration, stInput chan STEvent) {
	folderPath := expandTilde(folder.Path)
	ignorePatterns := getIgnorePatterns(folder.ID)
	fsInput := make(chan string)
	c := make(chan notify.EventInfo, maxFiles)
	if err := notify.Watch(folderPath+"/...", c, notify.All); err != nil {
		Warning.Println("Failed to install inotify handlers", err)
		informError("Failed to install inotify handler for " + folder.ID)
		return
	}
	defer notify.Stop(c)
	go accumulateChanges(debounceTimeout, folder.ID, folderPath, dirVsFiles, stInput, fsInput, informChange)
	OK.Println("Watching " + folder.ID + ": " + folderPath)
	if folder.RescanIntervalS < 1800 {
		OK.Printf("The rescan interval of folder %s can be increased to 3600 (an hour) or even 86400 (a day) as changes should be observed immediately while syncthing-inotify is running.", folder.ID)
	}
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

func waitForEvent(c chan notify.EventInfo) string {
	select {
	case ev, ok := <-c:
		if !ok {
			Warning.Println("Error: channel closed")
		}
		return ev.Path()
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
		Warning.Printf("Cannot connect to Syncthing, Status %d != 404 for POST\n", res.StatusCode, string(body))
		return errors.New("Invalid HTTP status code")
	}
	return nil
}

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
	if res.StatusCode != 200 {
		Warning.Printf("Error: Status %d != 200 for POST.\n%v: %v %v", msg, res.StatusCode)
		return errors.New("Invalid HTTP status code")
	}
	return err
}

func informChange(folder string, subs []string) error {
	data := url.Values{}
	data.Set("folder", folder)
	for _, sub := range subs {
		data.Add("sub", sub)
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
	if res.StatusCode != 200 {
		Warning.Printf("Error: Status %d != 200 for POST.\n%v: %v %v", folder, res.StatusCode)
		return errors.New("Invalid HTTP status code")
	} else {
		OK.Printf("Syncthing is indexing change in %v: %v", folder, subs)
	}
	// Wait until scan finishes
	_, err = ioutil.ReadAll(res.Body)
	return err
}

func accumulateChanges(interval time.Duration,
	folder string, folderPath string, dirVsFiles int,
	stInput chan STEvent, fsInput chan string,
	callback func(folder string, subs []string) error) func(string) {
	inProgress := make(map[string]bool) // [Path string, InProgress bool]
	currInterval := interval
	for {
		select {
		case item := <-stInput:
			if item.Path == "" {
				// Prepare for incoming changes
				currInterval = remoteIndexTimeout
				Debug.Println("[ST] Incoming Changes, increasing inotify timeout parameters")
				continue
			}
			if item.Finished {
				// Ensure path is cleared when receiving itemFinished
				delete(inProgress, item.Path)
				Debug.Println("[ST] Removed tracking for: " + item.Path)
				continue
			}
			if len(inProgress) > maxFiles {
				Debug.Println("[ST] Tracking too many files, aggregating STEvent: " + item.Path)
				continue
			}
			Debug.Println("[ST] Incoming: " + item.Path)
			inProgress[item.Path] = true
		case item := <-fsInput:
			p, ok := inProgress[item]
			if p && ok {
				// Change originated from ST
				delete(inProgress, item)
				Debug.Println("[FS] Removed tracking for: " + item)
				continue
			}
			if len(inProgress) > maxFiles {
				Debug.Println("[FS] Tracking too many files, aggregating FSEvent: " + item)
				continue
			}
			Debug.Println("[FS] Tracking: " + item)
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
						Debug.Println("Informing for: " + path)
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
					Debug.Println("[INFORMED] Removed tracking for: " + path)
				}
			}
		}
	}
}

func aggregateChanges(folder string, folderPath string, dirVsFiles int, callback func(folder string, folderPaths []string) error, paths []string) error {
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
		path := filepath.Clean(paths[i])
		if path == previousPath {
			continue
		}
		previousPath = path
		fi, _ := os.Stat(path)
		path = strings.TrimPrefix(path, folderPath)
		path = strings.TrimPrefix(path, string(os.PathSeparator))
		var dir string
		if fi != nil && fi.IsDir() {
			// Is directory itself, should definitely inform
			dir = path
			trackedPaths[path] = dirVsFiles
		} else {
			// Files are linked to -1 scores
			// Also increment the parent path with 1
			dir = filepath.Dir(path)
			if dir == "." {
				dir = ""
			}
			trackedPaths[path] = -1
			trackedPaths[dir] += 1
		}
		// Search for existing parent directory relations in the map
		for trackedPath, _ := range trackedPaths {
			if strings.HasPrefix(dir, trackedPath) {
				// Increment score of tracked current/parent directory
				trackedPaths[trackedPath] += 1 // for each file
			}
		}
	}
	var keys []string
	for k := range trackedPaths {
		keys = append(keys, k)
	}
	sort.Strings(keys) // Sort directories before their own files
	previousDone, previousPath := false, ""
	var scans []string
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
		scans = append(scans, trackedPath)
	}
	return callback(folder, scans)
}

func watchSTEvents(stChans map[string]chan STEvent, folders []FolderConfiguration) {
	lastSeenID := 0
	for {
		events, err := getSTEvents(lastSeenID)
		if err != nil {
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

func getSTConfig() (STConfig, error) {
	var path = filepath.Join(getSTDefaultConfDir(), "config.xml")
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

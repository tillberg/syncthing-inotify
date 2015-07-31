// syncwatcher_test.go
package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var (
	slash         = string(os.PathSeparator)
	testDirectory = "test" + slash
)

func clearTestDir() {
	os.RemoveAll(testDirectory)
}

func initTestDir() {
	clearTestDir()
	os.MkdirAll(testDirectory, 0755)
}

func createTestPaths(t *testing.T, fs ...string) []string {
	rs := make([]string, len(fs))
	for i, f := range fs {
		rs[i] = createTestPath(t, f)
	}
	return rs
}

func createTestPath(t *testing.T, f string) string {
	if strings.HasSuffix(f, slash) {
		err := os.MkdirAll(testDirectory+f, 0755)
		if err != nil && !os.IsExist(err) {
			t.Error("Failed to create test directory", err)
		}
		return strings.TrimSuffix(f, slash)
	} else {
		err := os.MkdirAll(filepath.Dir(testDirectory+f), 0755)
		if err != nil && !os.IsExist(err) {
			t.Error("Failed to create test directory", err)
		}
	}
	h, err := os.Create(testDirectory + f)
	if err != nil {
		t.Error("Failed to create test file", err)
	}
	h.Close()
	return f
}

func TestDebouncedFileWatch(t *testing.T) {
	// Log file change
	testOK := false
	testRepo := "test1"
	testFile := "a" + slash + "file1"
	testFiles := createTestPaths(t,
		testFile)
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != testFile {
			t.Error("Invalid result for file change: "+repo, sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDebouncedDirectoryWatch(t *testing.T) {
	// Log directory change
	testOK := false
	testRepo := "test1"
	testFile := createTestPath(t, "a"+slash)
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != testFile {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	fsChan <- testDirectory + testFile
	time.Sleep(testDebounceTimeout * 10)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDebouncedParentDirectoryWatch(t *testing.T) {
	// Convert a/file1.txt a/file2 a/file3.ogg to a
	testOK := false
	testRepo := "test1"
	testChangeDir := "a" + slash
	testFiles := createTestPaths(t,
		testChangeDir+"file1.txt",
		testChangeDir+"file2",
		testChangeDir+"file3.ogg")
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 2
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != "a" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDebouncedParentDirectoryWatch2(t *testing.T) {
	// Convert a a/file1.txt a/file2 b a/file3.ogg to a b
	testOK := 0
	testRepo := "test1"
	testChangeDir1 := "a" + slash
	testChangeDir2 := "b" + slash
	testFiles := createTestPaths(t,
		testChangeDir1,
		testChangeDir1+"file1.txt",
		testChangeDir1+"file2",
		testChangeDir2,
		testChangeDir1+"file3.ogg")
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 2 || sub[0] != "a" {
			t.Error("Invalid result for directory change 1: "+repo, sub)
		}
		if repo != testRepo || sub[1] != "b" {
			t.Error("Invalid result for directory change 2: "+repo, sub)
		}
		testOK = len(sub)
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if testOK != 2 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch3(t *testing.T) {
	// Don't convert a/b/file1.txt a/c/file2 a/d/file3.ogg
	testOK := 0
	testRepo := "test1"
	testFiles := createTestPaths(t,
		"a"+slash+"b"+slash+"file1.txt",
		"a"+slash+"c"+slash+"file2",
		"a"+slash+"d"+slash+"file3.ogg")
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		for i, s := range sub {
			if repo != testRepo || s != testFiles[i] {
				t.Error("Invalid result for directory change " + strconv.Itoa(testOK) + ": " + repo + " " + s)
			}
		}
		testOK = len(sub)
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if testOK != 3 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch4(t *testing.T) {
	// Convert a/e a/b/d a/b/file1.txt a/b/file2 a/b/file3.ogg a/b/c/file4 to a/b a/e
	testOK := 0
	testRepo := "test1"
	testFiles := createTestPaths(t,
		"a"+slash+"e",
		"a"+slash+"b"+slash+"d",
		"a"+slash+"b"+slash+"file1.txt",
		"a"+slash+"b"+slash+"file2",
		"a"+slash+"b"+slash+"file3.ogg",
		"a"+slash+"b"+slash+"c"+slash+"file4")
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 2 || sub[0] != "a"+slash+"b" {
			t.Error("Invalid result for directory change "+strconv.Itoa(testOK)+": "+repo, sub)
		}
		if repo != testRepo || sub[1] != "a"+slash+"e" {
			t.Error("Invalid result for directory change "+strconv.Itoa(testOK)+": "+repo, sub)
		}
		testOK = len(sub)
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if testOK != 2 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch5(t *testing.T) {
	// Convert a/b a/c file1 file2 file3 to _ (main folder)
	testOK := false
	testRepo := "test1"
	testFiles := createTestPaths(t,
		"a"+slash+"b"+slash,
		"a"+slash+"c"+slash,
		"file1",
		"file2",
		"file3")
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != "" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if !testOK {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch6(t *testing.T) {
	// Convert a/b/c a/b/c/f1 a/b/c/f2 a/b/c/f3 to a/b/c
	testOK := 0
	testRepo := "test1"
	testChangeDir := "a" + slash + "b" + slash + "c" + slash
	testFiles := createTestPaths(t,
		testChangeDir,
		testChangeDir+"file1.txt",
		testChangeDir+"file2",
		testChangeDir+"file3.ogg")
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != strings.TrimSuffix(testChangeDir, slash) {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		testOK += 1
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if testOK != 1 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryRemovedWatch(t *testing.T) {
	// Convert a a/b a/b/test.txt into a
	testOK := 0
	testRepo := "test1"
	testFiles := createTestPaths(t,
		"a"+slash,
		"a"+slash+"b"+slash,
		"a"+slash+"b"+slash+"file1.txt")
	clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != "a" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		testOK += 1
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if testOK != 1 {
		t.Error("Callback not correctly triggered")
	}
}

func TestSTEvents(t *testing.T) {
	// Ignore notifications if ST created them
	testOK := true
	testRepo := "test1"
	testFiles := createTestPaths(t,
		"file1",
		"file2",
		"file3")
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 0 {
			t.Error("Invalid result for directory change: " + repo)
		}
		testOK = false
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	stChan <- STEvent{Path: ""}
	for i := range testFiles {
		stChan <- STEvent{Path: testDirectory + testFiles[i], Finished: false}
		fsChan <- testDirectory + testFiles[i]
		stChan <- STEvent{Path: testDirectory + testFiles[i], Finished: true}
	}
	time.Sleep(testDebounceTimeout * 50)
	if !testOK {
		t.Error("Callback not correctly triggered")
	}
}

func TestFilesAggregation(t *testing.T) {
	nrFiles := 50
	testOK := false
	testRepo := "test1"
	testFiles := make([]string, nrFiles)
	for i := 0; i < nrFiles; i++ {
		testFiles[i] = createTestPath(t, "a"+slash+strconv.Itoa(i))
	}
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := nrFiles + 1
	stop := make(chan int, 1)
	stChan := make(chan STEvent, nrFiles)
	fsChan := make(chan string, nrFiles)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 50 || sub[0] != "a/0" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		if testOK {
			t.Error("Callback triggered multiple times")
		}
		testOK = true
		close(stop)
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for _, testFile := range testFiles {
		fsChan <- testDirectory + testFile
	}
	<-stop
	time.Sleep(250 * time.Millisecond)
	if !testOK {
		t.Error("Callback not triggered")
	}
}
func TestManyFilesAggregation(t *testing.T) {
	nrFiles := 5000
	testOK := false
	testRepo := "test1"
	testFiles := make([]string, nrFiles)
	for i := 0; i < nrFiles; i++ {
		testFiles[i] = createTestPath(t, "a"+slash+strconv.Itoa(i))
	}
	defer clearTestDir()
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stop := make(chan int, 1)
	stChan := make(chan STEvent, nrFiles)
	fsChan := make(chan string, nrFiles)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != "" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		if testOK {
			t.Error("Callback triggered multiple times")
		}
		testOK = true
		close(stop)
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for _, testFile := range testFiles {
		fsChan <- testDirectory + testFile
	}
	<-stop
	time.Sleep(250 * time.Millisecond)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDeletesAggregation(t *testing.T) {
	nrFiles := 50
	testOK := false
	testRepo := "test1"
	testFiles := make([]string, nrFiles)
	for i := 0; i < nrFiles; i++ {
		testFiles[i] = "a" + slash + strconv.Itoa(i)
	}
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stop := make(chan int, 1)
	stChan := make(chan STEvent, nrFiles)
	fsChan := make(chan string, nrFiles)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 50 || sub[0] != "a/0" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		if testOK {
			t.Error("Callback triggered multiple times")
		}
		testOK = true
		close(stop)
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for _, testFile := range testFiles {
		fsChan <- testDirectory + testFile
	}
	<-stop
	time.Sleep(250 * time.Millisecond)
	if !testOK {
		t.Error("Callback not triggered")
	}
}
func TestManyDeletesAggregation(t *testing.T) {
	nrFiles := 5000
	testOK := false
	testRepo := "test1"
	testFiles := make([]string, nrFiles)
	for i := 0; i < nrFiles; i++ {
		testFiles[i] = "a" + slash + strconv.Itoa(i)
	}
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stop := make(chan int, 1)
	stChan := make(chan STEvent, nrFiles)
	fsChan := make(chan string, nrFiles)
	fileChange := func(repo string, sub []string) error {
		if len(sub) == 1 && sub[0] == ".stfolder" {
			return nil
		}
		if repo != testRepo || len(sub) != 1 || sub[0] != "" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		if testOK {
			t.Error("Callback triggered multiple times")
		}
		testOK = true
		close(stop)
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for _, testFile := range testFiles {
		fsChan <- testDirectory + testFile
	}
	<-stop
	time.Sleep(250 * time.Millisecond)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

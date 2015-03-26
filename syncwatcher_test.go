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
	slash = string(os.PathSeparator)
)

func initTestDir() string {
	dir := "test" + slash
	os.RemoveAll(dir)
	os.MkdirAll(dir, 0755)
	return dir
}

func createTestPaths(t *testing.T, dir string, fs ...string) []string {
	rs := make([]string, len(fs))
	for i, f := range fs {
		rs[i] = createTestPath(t, dir, f)
	}
	return rs
}

func createTestPath(t *testing.T, dir string, f string) string {
	if strings.HasSuffix(f, slash) {
		err := os.MkdirAll(dir+f, 0755)
		if err != nil && !os.IsExist(err) {
			t.Error("Failed to create test directory", err)
		}
		return strings.TrimSuffix(f, slash)
	} else {
		err := os.MkdirAll(filepath.Dir(dir+f), 0755)
		if err != nil && !os.IsExist(err) {
			t.Error("Failed to create test directory", err)
		}
	}
	h, err := os.Create(dir + f)
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
	testDirectory := initTestDir()
	testFile := "a" + slash + "file1"
	testDebounceTimeout := 2 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if repo != testRepo || sub != testFile {
			t.Error("Invalid result for file change: " + repo + " " + sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	fsChan <- testDirectory + slash + testFile
	time.Sleep(testDebounceTimeout * 10)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDebouncedDirectoryWatch(t *testing.T) {
	// Log directory change
	testOK := false
	testRepo := "test1"
	testDirectory := initTestDir()
	testFile := createTestPath(t, testDirectory, "a"+slash)
	testDebounceTimeout := 2 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if repo != testRepo || sub != testFile {
			t.Error("Invalid result for directory change: " + repo + " " + sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	fsChan <- testDirectory + slash + testFile
	time.Sleep(testDebounceTimeout * 10)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDebouncedParentDirectoryWatch(t *testing.T) {
	// Convert a/file1.txt a/file2 a/file3.ogg to a
	testOK := false
	testRepo := "test1"
	testDirectory := initTestDir()
	testChangeDir := "a" + slash
	testFiles := createTestPaths(t, testDirectory,
		testChangeDir+"file1.txt",
		testChangeDir+"file2",
		testChangeDir+"file3.ogg")
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if repo != testRepo || sub != "a" {
			t.Error("Invalid result for directory change: " + repo + " " + sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + slash + testFiles[i]
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
	testDirectory := initTestDir()
	testChangeDir1 := "a" + slash
	testChangeDir2 := "b" + slash
	testFiles := createTestPaths(t, testDirectory, testChangeDir1, testChangeDir1+"file1.txt", testChangeDir1+"file2", testChangeDir2, testChangeDir1+"file3.ogg")
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if testOK == 0 {
			if repo != testRepo || sub != "a" {
				t.Error("Invalid result for directory change 1: " + repo + " " + sub)
			}
		} else if testOK == 1 {
			if repo != testRepo || sub != "b" {
				t.Error("Invalid result for directory change 2: " + repo + " " + sub)
			}
		}
		testOK += 1
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + slash + testFiles[i]
	}
	time.Sleep(testDebounceTimeout * 10)
	if testOK != 2 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch3(t *testing.T) {
	// Convert a/b/file1.txt a/c/file2 a/d/file3.ogg to a/b a/c a/d
	testOK := 0
	testRepo := "test1"
	testDirectory := initTestDir()
	testFiles := createTestPaths(t, testDirectory,
		"a"+slash+"b"+slash+"file1.txt",
		"a"+slash+"c"+slash+"file2",
		"a"+slash+"d"+slash+"file3.ogg")
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if repo != testRepo || sub != testFiles[testOK] {
			t.Error("Invalid result for directory change " + strconv.Itoa(testOK) + ": " + repo + " " + sub)
		}
		testOK += 1
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + slash + testFiles[i]
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
	testDirectory := initTestDir()
	testFiles := createTestPaths(t, testDirectory,
		"a"+slash+"e",
		"a"+slash+"b"+slash+"d",
		"a"+slash+"b"+slash+"file1.txt",
		"a"+slash+"b"+slash+"file2",
		"a"+slash+"b"+slash+"file3.ogg",
		"a"+slash+"b"+slash+"c"+slash+"file4")
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if testOK == 0 {
			if repo != testRepo || sub != "a"+slash+"b" {
				t.Error("Invalid result for directory change " + strconv.Itoa(testOK) + ": " + repo + " " + sub)
			}
		}
		if testOK == 1 {
			if repo != testRepo || sub != "a"+slash+"e" {
				t.Error("Invalid result for directory change " + strconv.Itoa(testOK) + ": " + repo + " " + sub)
			}
		}
		testOK += 1
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + slash + testFiles[i]
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
	testDirectory := initTestDir()
	testFiles := createTestPaths(t, testDirectory,
		"a"+slash+"b"+slash,
		"a"+slash+"c"+slash,
		"file1",
		"file2",
		"file3")
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if repo != testRepo || sub != "" {
			t.Error("Invalid result for directory change: "+repo, sub)
		}
		testOK = true
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + slash + testFiles[i]
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
	testDirectory := initTestDir()
	testChangeDir := "a" + slash + "b" + slash + "c" + slash
	testFiles := createTestPaths(t, testDirectory,
		testChangeDir,
		testChangeDir+"file1.txt",
		testChangeDir+"file2",
		testChangeDir+"file3.ogg")
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if repo != testRepo || sub != strings.TrimSuffix(testChangeDir, slash) {
			t.Error("Invalid result for directory change: " + repo + " " + sub)
		}
		testOK += 1
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	for i := range testFiles {
		fsChan <- testDirectory + slash + testFiles[i]
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
	testDirectory := initTestDir()
	testFiles := createTestPaths(t, testDirectory,
		"file1",
		"file2",
		"file3")
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 10
	stChan := make(chan STEvent, 10)
	fsChan := make(chan string, 10)
	fileChange := func(repo string, sub string) error {
		if repo != testRepo || sub != "" {
			t.Error("Invalid result for directory change: " + repo)
		}
		testOK = false
		return nil
	}
	go accumulateChanges(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, stChan, fsChan, fileChange)
	stChan <- STEvent{Path: ""}
	for i := range testFiles {
		stChan <- STEvent{Path: testDirectory + slash + testFiles[i], Finished: false}
		fsChan <- testDirectory + slash + testFiles[i]
		stChan <- STEvent{Path: testDirectory + slash + testFiles[i], Finished: true}
	}
	time.Sleep(testDebounceTimeout * 10)
	if !testOK {
		t.Error("Callback not correctly triggered")
	}
}

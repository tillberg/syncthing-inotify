// syncwatcher_test.go
package main

import (
	"testing"
	"time"
	"os"
	"strconv"
)

var (
	slash = string(os.PathSeparator)
)

func TestDebouncedFileWatch(t *testing.T) {
	// Log file change
	testOK := false
	testRepo := "test1"
	testDirectory := getHomeDir() + slash + "Sync"
	testFile := "a"+slash+"file1"
	testDebounceTimeout := 2 * time.Millisecond
	testDirVsFiles := 10
	fileChange := func(repo string, sub string) {
		if repo != testRepo || sub != testFile {
			t.Error("Invalid result for file change: "+repo+" "+sub)
		}
		testOK = true
	}
	informChangeDebounced := informChangeDebounce(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, fileChange)
	informChangeDebounced(testDirectory+slash+testFile)
	time.Sleep(testDebounceTimeout*2)
	if !testOK {
		t.Error("Callback not triggered")
	}
}


func TestDebouncedDirectoryWatch(t *testing.T) {
	// Log directory change
	testOK := false
	testRepo := "test1"
	testDirectory := getHomeDir() + slash + "Sync"
	testFile := "a"+slash
	testDebounceTimeout := 2 * time.Millisecond
	testDirVsFiles := 10
	fileChange := func(repo string, sub string) {
		if repo != testRepo || sub+slash != testFile {
			t.Error("Invalid result for directory change: "+repo+" "+sub)
		}
		testOK = true
	}
	informChangeDebounced := informChangeDebounce(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, fileChange)
	informChangeDebounced(testDirectory+slash+testFile)
	time.Sleep(testDebounceTimeout*2)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDebouncedParentDirectoryWatch(t *testing.T) {
	// Convert a/file1.txt a/file2 a/file3.ogg to a
	testOK := false
	testRepo := "test1"
	testDirectory := getHomeDir() + slash + "Sync"
	testChangeDir := "a"+slash
	testFiles := [...]string{ testChangeDir+"file1.txt", testChangeDir+"file2", testChangeDir+"file3.ogg" }
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	fileChange := func(repo string, sub string) {
		if repo != testRepo || sub != "a" {
			t.Error("Invalid result for directory change: "+repo+" "+sub)
		}
		testOK = true
	}
	informChangeDebounced := informChangeDebounce(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, fileChange)
	for i := range testFiles {
		informChangeDebounced(testDirectory+slash+testFiles[i])
	}
	time.Sleep(testDebounceTimeout*2)
	if !testOK {
		t.Error("Callback not triggered")
	}
}

func TestDebouncedParentDirectoryWatch2(t *testing.T) {
	// Convert a a/file1.txt a/file2 b a/file3.ogg to a b
	testOK := 0
	testRepo := "test1"
	testDirectory := getHomeDir() + slash + "Sync"
	testChangeDir1 := "a"+slash
	testChangeDir2 := "b"+slash
	testFiles := [...]string{ testChangeDir1, testChangeDir1+"file1.txt", testChangeDir1+"file2", testChangeDir2, testChangeDir1+"file3.ogg" }
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	fileChange := func(repo string, sub string) {
		if testOK == 0 {
			if repo != testRepo || sub != "a" {
				t.Error("Invalid result for directory change 1: "+repo+" "+sub)
			}
		} else if testOK == 1 {
			if repo != testRepo || sub != "b" {
				t.Error("Invalid result for directory change 2: "+repo+" "+sub)
			}
		}
		testOK += 1
	}
	informChangeDebounced := informChangeDebounce(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, fileChange)
	for i := range testFiles {
		informChangeDebounced(testDirectory+slash+testFiles[i])
	}
	time.Sleep(testDebounceTimeout*2)
	if testOK != 2 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch3(t *testing.T) {
	// Convert a/b/file1.txt a/c/file2 a/d/file3.ogg to a/b a/c a/d
	testOK := 0
	testRepo := "test1"
	testDirectory := getHomeDir() + slash + "Sync"
	testFiles := [...]string{
		"a"+slash+"b"+slash+"file1.txt",
		"a"+slash+"c"+slash+"file2",
		"a"+slash+"d"+slash+"file3.ogg" }
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	fileChange := func(repo string, sub string) {
		if repo != testRepo || sub != testFiles[testOK] {
			t.Error("Invalid result for directory change "+strconv.Itoa(testOK)+": "+repo+" "+sub)
		}
		testOK += 1
	}
	informChangeDebounced := informChangeDebounce(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, fileChange)
	for i := range testFiles {
		informChangeDebounced(testDirectory+slash+testFiles[i])
	}
	time.Sleep(testDebounceTimeout*2)
	if testOK != 3 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch4(t *testing.T) {
	// Convert a/e a/b/d a/b/file1.txt a/b/file2 a/b/file3.ogg a/b/c/file4 to a/b a/e
	testOK := 0
	testRepo := "test1"
	testDirectory := getHomeDir() + slash + "Sync"
	testFiles := [...]string{
		"a"+slash+"e",
		"a"+slash+"b"+slash+"d",
		"a"+slash+"b"+slash+"file1.txt",
		"a"+slash+"b"+slash+"file2",
		"a"+slash+"b"+slash+"file3.ogg",
		"a"+slash+"b"+slash+"c"+slash+"file4" }
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	fileChange := func(repo string, sub string) {
		if testOK == 0 {
			if repo != testRepo || sub != "a"+slash+"b" {
				t.Error("Invalid result for directory change "+strconv.Itoa(testOK)+": "+repo+" "+sub)
			}
		}
		if testOK == 1 {
			if repo != testRepo || sub != "a"+slash+"e" {
				t.Error("Invalid result for directory change "+strconv.Itoa(testOK)+": "+repo+" "+sub)
			}
		}
		testOK += 1
	}
	informChangeDebounced := informChangeDebounce(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, fileChange)
	for i := range testFiles {
		informChangeDebounced(testDirectory+slash+testFiles[i])
	}
	time.Sleep(testDebounceTimeout*2)
	if testOK != 2 {
		t.Error("Callback not correctly triggered")
	}
}

func TestDebouncedParentDirectoryWatch5(t *testing.T) {
	// Convert a/b a/c file1 file2 file3 to _ (main folder)
	testOK := false
	testRepo := "test1"
	testDirectory := getHomeDir() + slash + "Sync"
	testFiles := [...]string{
		"a"+slash+"b",
		"a"+slash+"c",
		"file1",
		"file2",
		"file3" }
	testDebounceTimeout := 20 * time.Millisecond
	testDirVsFiles := 3
	fileChange := func(repo string, sub string) {
		if repo != testRepo || sub != "" {
			t.Error("Invalid result for directory change: "+repo)
		}
		testOK = true
	}
	informChangeDebounced := informChangeDebounce(testDebounceTimeout, testRepo, testDirectory, testDirVsFiles, fileChange)
	for i := range testFiles {
		informChangeDebounced(testDirectory+slash+testFiles[i])
	}
	time.Sleep(testDebounceTimeout*2)
	if !testOK {
		t.Error("Callback not correctly triggered")
	}
}


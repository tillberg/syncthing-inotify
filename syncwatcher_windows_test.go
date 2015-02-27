// syncwatcher_test.go
//+build windows
package main

import (
	"testing"
	"time"
	"os"
	"strconv"
	"encoding/json"
	"strings"
	"regexp"
)

func TestIgnores(t *testing.T) {
	s := `{"patterns":["(?i)^\\.DS_Store$","(?i)^.*\\\\\\.DS_Store$","(?i)^\\.DS_Store\\\\.*$","(?i)^.*\\\\\\.DS_Store\\\\.*$","(?i)^\\.Spotlight-V100$","(?i)^.*\\\\\\.Spotlight-V100$","(?i)^\\.Spotlight-V100\\\\.*$","(?i)^.*\\\\\\.Spotlight-V100\\\\.*$","(?i)^\\.Trashes$","(?i)^.*\\\\\\.Trashes$","(?i)^\\.Trashes\\\\.*$","(?i)^.*\\\\\\.Trashes\\\\.*$","(?i)^~[^\\\\]*$","(?i)^.*\\\\~[^\\\\]*$","(?i)^~[^\\\\]*\\\\.*$","(?i)^.*\\\\~[^\\\\]*\\\\.*$","(?i)^ehthumbs\\.db$","(?i)^.*\\\\ehthumbs\\.db$","(?i)^ehthumbs\\.db\\\\.*$","(?i)^.*\\\\ehthumbs\\.db\\\\.*$","(?i)^desktop\\.ini$","(?i)^.*\\\\desktop\\.ini$","(?i)^desktop\\.ini\\\\.*$","(?i)^.*\\\\desktop\\.ini\\\\.*$","(?i)^Thumbs\\.db$","(?i)^.*\\\\Thumbs\\.db$","(?i)^Thumbs\\.db\\\\.*$","(?i)^.*\\\\Thumbs\\.db\\\\.*$","(?i)^\\._[^\\\\]*$","(?i)^.*\\\\\\._[^\\\\]*$","(?i)^\\._[^\\\\]*\\\\.*$","(?i)^.*\\\\\\._[^\\\\]*\\\\.*$","(?i)^\\.sync$","(?i)^.*\\\\\\.sync$","(?i)^\\.sync\\\\.*$","(?i)^.*\\\\\\.sync\\\\.*$","(?i)^\\.git$","(?i)^.*\\\\\\.git$","(?i)^\\.git\\\\.*$","(?i)^.*\\\\\\.git\\\\.*$","(?i)^\\.Trash-1000$","(?i)^.*\\\\\\.Trash-1000$","(?i)^\\.Trash-1000\\\\.*$","(?i)^.*\\\\\\.Trash-1000\\\\.*$","(?i)^ignored folder$","(?i)^ignored file\\..*$"]}`
	var ignores map[string][]string
	err := json.Unmarshal([]byte(s), &ignores)
	if err != nil {
		t.Fatal(err)
	}
	ignorePaths := ignores["ignore"]
	ignorePatterns := make([]Pattern, len(ignores["patterns"]))
	for i, str := range ignores["patterns"] {
		pattern := strings.TrimPrefix(str, "(?exclude)")
		regexp, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatal(err)
		}
		ignorePatterns[i] = Pattern { regexp, str == pattern }
	}
	if !shouldIgnore("C:\\test", ignorePaths, ignorePatterns, "C:\\test\\.DS_Store") {
		t.Error("Should ignore this pattern")
	}
	if !shouldIgnore("C:\\test", ignorePaths, ignorePatterns, "C:\\test\\ignored folder") {
		t.Error("Should ignore this pattern")
	}
}



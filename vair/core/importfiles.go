package core

// Drag-and-drop file import.
//
// Dropping a file onto the window behaves like pasting its contents: a file of
// node URLs (or a base64 subscription body) is imported as configs, while a
// file that is just a list of http(s) links becomes tab SOURCES (each link is
// added and fetched). The same file can hold either — we decide by looking at
// the content, exactly like the paste path does.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// maxImportFileSize caps a dropped file (a config list is text; anything this
// large is not one, and reading it whole would spike memory).
const maxImportFileSize = 64 << 20 // 64 MiB

// ImportFiles imports one or more dropped files into the active tab, as SOURCES
// so they keep working after a refresh: a file of node URLs is registered in the
// tab's Files list (re-read from disk on every fetch, exactly like a file added
// through tab settings), and a file that is just a list of links contributes
// those links to the tab's URL sources. Everything is registered first and
// fetched ONCE, so the spinner stays up until the whole import is loaded.
// Emits a "dropped" event with a short summary so the UI can toast the result.
func ImportFiles(paths []string) {
	if len(paths) == 0 {
		return
	}
	// SOURCES is the built-in, read-only list — it holds no user sources, so a
	// drop there is refused rather than silently redirected to another tab. (The
	// UI also drops the drop-target attribute on that tab, so this is a backstop
	// for a drop that was already in flight.)
	tabID := activeTabID()
	if tabID == "" || tabID == "main" {
		vlog("warn", "drop: not available on the SOURCES tab")
		state.broadcast(SSEEvent{Type: "dropped", Payload: map[string]any{
			"message": "not available on the SOURCES tab",
		}, Tab: tabID})
		return
	}

	var urls []string
	var files []TabFile
	skipped := 0

	for _, p := range paths {
		body, err := readImportFile(p)
		if err != nil {
			skipped++
			vlog("warn", "drop: %s: %v", p, err)
			continue
		}
		if links := bareLinkLines(body); len(links) > 0 {
			urls = append(urls, links...) // a list of subscription links
			continue
		}
		files = append(files, TabFile{Name: filepath.Base(p), Path: p}) // a config file
	}

	if len(urls) == 0 && len(files) == 0 {
		msg := dropSummary(0, 0, 0, skipped)
		vlog("info", "drop: %s", msg)
		state.broadcast(SSEEvent{Type: "dropped", Payload: map[string]any{"message": msg}, Tab: tabID})
		return
	}

	// Keep the tab marked as loading across the whole import: register every
	// source, then fetch once (fetchTabURLs owns the flag for its own run).
	state.mu.Lock()
	state.fetching[tabID] = true
	state.mu.Unlock()
	state.broadcast(SSEEvent{Type: "loading", Payload: nil, Tab: tabID})

	addedURLs, addedFiles, allURLs, allFiles, ok := addTabSourcesNoFetch(tabID, urls, files)
	if !ok {
		state.mu.Lock()
		delete(state.fetching, tabID)
		state.mu.Unlock()
		loadedSignal(tabID)
		return
	}
	fetchTabURLs(tabID, allURLs, allFiles) // synchronous: spinner clears when done

	msg := dropSummary(addedFiles, addedURLs, len(urls), skipped)
	vlog("info", "drop: %s", msg)
	state.broadcast(SSEEvent{Type: "dropped", Payload: map[string]any{
		"files": addedFiles, "sources": addedURLs, "message": msg,
	}, Tab: tabID})
}

// readImportFile reads a droppable text file, rejecting directories, oversized
// files and binaries (a NUL byte in the first chunk means it isn't a text list).
func readImportFile(path string) (string, error) {
	st, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("is a directory")
	}
	if st.Size() > maxImportFileSize {
		return "", fmt.Errorf("file too large (%d MiB)", st.Size()>>20)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	head := b
	if len(head) > 8192 {
		head = head[:8192]
	}
	if strings.IndexByte(string(head), 0) >= 0 {
		return "", fmt.Errorf("not a text file")
	}
	return string(b), nil
}

// bareLinkLines returns every line of the file when ALL non-blank lines are
// bare http(s) links (a source list); otherwise nil, so mixed content and
// config bodies that merely contain a URL are treated as configs. Comment lines
// (# / //) are ignored so annotated lists still work.
func bareLinkLines(body string) []string {
	var urls []string
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(strings.TrimSuffix(ln, "\r"))
		if ln == "" || strings.HasPrefix(ln, "#") || strings.HasPrefix(ln, "//") {
			continue
		}
		if !isSubscriptionURL(ln) {
			return nil // not a pure link list
		}
		if !strInSlice(ln, urls) {
			urls = append(urls, ln)
		}
	}
	return urls
}

// dropSummary phrases the result for the toast (English; the UI translates).
func dropSummary(filesAdded, sourcesAdded, sourcesSeen, skipped int) string {
	var parts []string
	if filesAdded > 0 {
		parts = append(parts, fmt.Sprintf("files: %d", filesAdded))
	}
	if sourcesSeen > 0 {
		parts = append(parts, fmt.Sprintf("sources: %d/%d", sourcesAdded, sourcesSeen))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("skipped: %d", skipped))
	}
	if len(parts) == 0 {
		return "nothing to import"
	}
	return strings.Join(parts, ", ")
}

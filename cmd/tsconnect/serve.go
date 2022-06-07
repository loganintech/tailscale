// Copyright (c) 2022 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"net/http"
	"path"
	"time"

	"tailscale.com/tsweb"
)

//go:embed dist/* index.html
var embeddedFS embed.FS

var serveStartTime = time.Now()

func runServe() {
	mux := http.NewServeMux()

	indexBytes, err := generateServeIndex()
	if err != nil {
		log.Fatalf("Could not generate index.html: %v", err)
	}
	mux.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "index.html", serveStartTime, bytes.NewReader(indexBytes))
	}))
	mux.Handle("/dist/", http.HandlerFunc(handleServeDist))
	tsweb.Debugger(mux)

	log.Printf("Listening on %s", *addr)
	err = http.ListenAndServe(*addr, mux)
	if err != nil {
		log.Fatal(err)
	}
}

func generateServeIndex() ([]byte, error) {
	log.Printf("Generating index.html...\n")
	rawIndexBytes, err := embeddedFS.ReadFile("index.html")
	if err != nil {
		return nil, fmt.Errorf("Could not read index.html: %w", err)
	}

	entryPointMapFile, err := embeddedFS.Open("dist/entry-point-map.json")
	if err != nil {
		return nil, fmt.Errorf("Could not open entry-point-map.json: %w", err)
	}
	defer entryPointMapFile.Close()
	entryPointMapBytes, err := ioutil.ReadAll(entryPointMapFile)
	if err != nil {
		return nil, fmt.Errorf("Could not read entry-point-map.json: %w", err)
	}
	var entryPointMap EntryPointMap
	if err := json.Unmarshal(entryPointMapBytes, &entryPointMap); err != nil {
		return nil, fmt.Errorf("Could not parse entry-point-map.json: %w", err)
	}

	indexBytes := rawIndexBytes
	for entryPointPath, defaultDistPath := range entryPointsToDistPaths {
		hashedDistPath := entryPointMap[entryPointPath]
		if hashedDistPath != "" {
			indexBytes = bytes.ReplaceAll(indexBytes, []byte(defaultDistPath), []byte(hashedDistPath))
		}
	}

	return indexBytes, nil
}

var entryPointsToDistPaths = map[string]string{
	"src/index.css": "dist/index.css",
	"src/index.js":  "dist/index.js",
}

func handleServeDist(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path[1:]
	var f fs.File
	// Prefer pre-compressed versions generated during the build step.
	if tsweb.AcceptsEncoding(r, "br") {
		if brotliFile, err := embeddedFS.Open(p + ".br"); err == nil {
			f = brotliFile
			w.Header().Set("Content-Encoding", "br")
		}
	}
	if f == nil && tsweb.AcceptsEncoding(r, "gzip") {
		if gzipFile, err := embeddedFS.Open(p + ".gz"); err == nil {
			f = gzipFile
			w.Header().Set("Content-Encoding", "gzip")
		}
	}

	if f == nil {
		if rawFile, err := embeddedFS.Open(r.URL.Path[1:]); err == nil {
			f = rawFile
		} else {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}
	defer f.Close()

	// fs.File does not claim to implement Seeker, but in practice it does.
	fSeeker, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "Not seekable", http.StatusInternalServerError)
		return
	}

	// Aggressively cache static assets, since we cache-bust our assets with
	// hashed filenames.
	w.Header().Set("Cache-Control", "public, max-age=31535996")
	w.Header().Set("Vary", "Accept-Encoding")

	http.ServeContent(w, r, path.Base(r.URL.Path), serveStartTime, fSeeker)
}

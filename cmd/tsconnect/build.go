// Copyright (c) 2022 Tailscale Inc & AUTHORS All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"io/fs"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"

	"github.com/andybalholm/brotli"
	esbuild "github.com/evanw/esbuild/pkg/api"
	"golang.org/x/sync/errgroup"
)

func runBuild() {
	buildOptions, err := commonSetup(prodMode)
	if err != nil {
		log.Fatalf("Cannot setup: %v", err)
	}

	if err := cleanDist(); err != nil {
		log.Fatalf("Cannot clean dist/: %v", err)
	}

	buildOptions.Write = true
	buildOptions.MinifyWhitespace = true
	buildOptions.MinifyIdentifiers = true
	buildOptions.MinifySyntax = true

	buildOptions.EntryNames = "[dir]/[name]-[hash]"
	buildOptions.AssetNames = "[name]-[hash]"
	buildOptions.Metafile = true

	log.Printf("Running esbuild...\n")
	result := esbuild.Build(*buildOptions)
	if len(result.Errors) > 0 {
		log.Printf("ESBuild Error:\n")
		for _, e := range result.Errors {
			log.Printf("%v", e)
		}
		log.Fatal("Build failed")
	}
	if len(result.Warnings) > 0 {
		log.Printf("ESBuild Warnings:\n")
		for _, w := range result.Warnings {
			log.Printf("%v", w)
		}
	}

	// Extract hashed file names from the build metadata.
	var metadata EsbuildMetadata
	if err := json.Unmarshal([]byte(result.Metafile), &metadata); err != nil {
		log.Fatalf("Cannot parse esbuild metadata: %v", err)
	}
	entryPointMap := make(EntryPointMap)
	for outputPath, output := range metadata.Outputs {
		if output.EntryPoint != "" {
			entryPointMap[output.EntryPoint] = outputPath
		}
	}
	if jsonEntryPointMap, err := json.Marshal(entryPointMap); err == nil {
		if err := ioutil.WriteFile("./dist/entry-point-map.json", jsonEntryPointMap, 0666); err != nil {
			log.Fatalf("Cannot write entry point map: %v", err)
		}
	} else {
		log.Fatalf("Cannot marshal entry point map: %v", err)
	}

	if er := precompressDist(); err != nil {
		log.Fatalf("Cannot precompress resources: %v", er)
	}
}

// EsbuildMetadata is the subset of metadata struct (described by
// https://esbuild.github.io/api/#metafile) that we care about for mapping
// from entry points to hashed file names.
type EsbuildMetadata = struct {
	Outputs map[string]struct {
		EntryPoint string `json:"entryPoint,omitempty"`
	} `json:"outputs,omitempty"`
}

// cleanDist removes files from the dist build directory, except the placeholder
// one that we keep to make sure Git still creates the directory.
func cleanDist() error {
	log.Printf("Cleaning dist/...\n")
	files, err := os.ReadDir("dist")
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.Name() != "placeholder" {
			if err := os.Remove(filepath.Join("dist", file.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}

func precompressDist() error {
	log.Printf("Pre-compressing files in dist/...\n")
	var eg errgroup.Group
	err := fs.WalkDir(os.DirFS("./"), "dist", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !compressibleExtensions[filepath.Ext(path)] {
			return nil
		}
		log.Printf("Pre-compressing %v\n", path)

		eg.Go(func() error {
			return precompress(path)
		})
		return nil
	})
	if err != nil {
		return err
	}
	return eg.Wait()
}

var compressibleExtensions = map[string]bool{
	".js":   true,
	".css":  true,
	".wasm": true,
}

func precompress(path string) error {
	contents, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	fi, err := os.Lstat(path)
	if err != nil {
		return err
	}

	err = writeCompressed(contents, func(w io.Writer) (io.WriteCloser, error) {
		return gzip.NewWriterLevel(w, gzip.BestCompression)
	}, path+".gz", fi.Mode())
	if err != nil {
		return err
	}
	return writeCompressed(contents, func(w io.Writer) (io.WriteCloser, error) {
		return brotli.NewWriterLevel(w, brotli.BestCompression), nil
	}, path+".br", fi.Mode())
}

func writeCompressed(contents []byte, compressedWriterCreator func(io.Writer) (io.WriteCloser, error), outputPath string, outputMode fs.FileMode) error {
	var buf bytes.Buffer
	compressedWriter, err := compressedWriterCreator(&buf)
	if err != nil {
		return err
	}
	if _, err := compressedWriter.Write(contents); err != nil {
		return err
	}
	if err := compressedWriter.Close(); err != nil {
		return err
	}
	return os.WriteFile(outputPath, buf.Bytes(), outputMode)
}

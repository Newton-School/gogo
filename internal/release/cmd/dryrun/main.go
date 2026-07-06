package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/Newton-School/gogo/internal/release"
)

func main() {
	tag := flag.String("tag", "", "release tag, for example v0.1.0")
	commit := flag.String("commit", "", "source commit")
	buildDate := flag.String("build-date", "", "RFC3339 build timestamp")
	changelog := flag.String("changelog", "CHANGELOG.md", "path to CHANGELOG.md")
	notesOut := flag.String("notes-out", "", "optional path to write release notes")
	flag.Parse()

	date := *buildDate
	if date == "" {
		date = time.Now().UTC().Format(time.RFC3339)
	}

	markdown, err := os.ReadFile(*changelog)
	if err != nil {
		exitf("read changelog: %v", err)
	}
	notes, err := release.ChangelogEntry(string(markdown), *tag)
	if err != nil {
		exitf("read release notes: %v", err)
	}
	plan, err := release.NewPlan(*tag, *commit, date)
	if err != nil {
		exitf("build release plan: %v", err)
	}
	if *notesOut != "" {
		if err := os.WriteFile(*notesOut, []byte(notes+"\n"), 0o644); err != nil {
			exitf("write release notes: %v", err)
		}
	}
	if err := release.WriteDryRun(os.Stdout, plan, notes); err != nil {
		exitf("write dry run: %v", err)
	}
}

func exitf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

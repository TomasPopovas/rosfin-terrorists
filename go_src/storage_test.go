package main

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func makeZip(t *testing.T, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestRunDir(t *testing.T) {
	at := time.Date(2026, 8, 17, 9, 5, 0, 0, time.UTC)
	got := RunDir("downloads", at)
	want := filepath.Join("downloads", "2026-08-17_09-05")
	if got != want {
		t.Fatalf("RunDir = %q, want %q", got, want)
	}
}

func TestSaveArchiveAndExtract(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "2026-08-17_09-05")
	data := makeZip(t, map[string]string{"perechen.xml": "<list/>"})

	saved, err := SaveArchive(dir, FormatXML, data, ".zip")
	if err != nil {
		t.Fatal(err)
	}
	if saved.Archive != "perechen_xml.zip" {
		t.Fatalf("archive name = %q", saved.Archive)
	}
	if saved.Bytes != len(data) || len(saved.SHA256) != 64 {
		t.Fatalf("unexpected metadata: %+v", saved)
	}
	if _, err := os.Stat(filepath.Join(dir, saved.Archive)); err != nil {
		t.Fatal(err)
	}

	names, err := ExtractZip(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "perechen.xml" {
		t.Fatalf("extracted = %v", names)
	}
	content, err := os.ReadFile(filepath.Join(dir, "perechen.xml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "<list/>" {
		t.Fatalf("content = %q", content)
	}
}

func TestExtractZipIgnoresTraversal(t *testing.T) {
	dir := t.TempDir()
	data := makeZip(t, map[string]string{"../../evil.xml": "boom"})

	names, err := ExtractZip(dir, data)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 1 || names[0] != "evil.xml" {
		t.Fatalf("names = %v", names)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil.xml")); err != nil {
		t.Fatalf("файл должен остаться внутри папки запуска: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "evil.xml")); err == nil {
		t.Fatal("zip slip: файл записан за пределами папки")
	}
}

func TestPruneOldRuns(t *testing.T) {
	base := t.TempDir()
	runs := []string{"2026-08-15_09-00", "2026-08-16_09-00", "2026-08-17_09-00", "2026-08-17_21-00"}
	for _, r := range runs {
		if err := os.MkdirAll(filepath.Join(base, r), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(base, "not-a-run"), 0o755); err != nil {
		t.Fatal(err)
	}

	removed, err := PruneOldRuns(base, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 2 || removed[0] != "2026-08-15_09-00" || removed[1] != "2026-08-16_09-00" {
		t.Fatalf("removed = %v", removed)
	}
	for _, keep := range []string{"2026-08-17_09-00", "2026-08-17_21-00", "not-a-run"} {
		if _, err := os.Stat(filepath.Join(base, keep)); err != nil {
			t.Fatalf("папка %s не должна была удаляться: %v", keep, err)
		}
	}

	if removed, err := PruneOldRuns(base, 0); err != nil || removed != nil {
		t.Fatalf("keep=0 должен отключать очистку, got %v %v", removed, err)
	}
}

func TestExtFromDisposition(t *testing.T) {
	cases := map[string]string{
		`attachment; filename="perechen.zip"`: ".zip",
		`attachment; filename=list.xml`:       ".xml",
		``:                                    ".zip",
		`attachment`:                          ".zip",
	}
	for in, want := range cases {
		if got := extFromDisposition(in); got != want {
			t.Fatalf("extFromDisposition(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsHTML(t *testing.T) {
	if !isHTML([]byte("<!DOCTYPE html><html>")) {
		t.Fatal("страница логина должна распознаваться как HTML")
	}
	if isHTML([]byte{0x50, 0x4b, 0x03, 0x04}) {
		t.Fatal("zip не должен распознаваться как HTML")
	}
}

func TestUpdateLatestLinkIsRelative(t *testing.T) {
	base := t.TempDir()
	run := filepath.Join(base, "2026-08-17_09-00")
	if err := os.MkdirAll(run, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(run, "perechen.xml"), []byte("<list/>"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := UpdateLatestLink(base, run); err != nil {
		t.Fatal(err)
	}

	target, err := os.Readlink(filepath.Join(base, "latest"))
	if err != nil {
		t.Fatal(err)
	}
	// Ссылка должна быть относительной, иначе она битая при просмотре
	// смонтированной папки с хоста (контейнер видит /data, хост — другой путь).
	if filepath.IsAbs(target) {
		t.Fatalf("симлинк latest абсолютный (%q) — на хосте он будет битым", target)
	}
	if target != "2026-08-17_09-00" {
		t.Fatalf("target = %q", target)
	}
	if _, err := os.ReadFile(filepath.Join(base, "latest", "perechen.xml")); err != nil {
		t.Fatalf("через latest файл не читается: %v", err)
	}

	// Повторный вызов должен переписывать существующую ссылку.
	run2 := filepath.Join(base, "2026-08-17_21-00")
	if err := os.MkdirAll(run2, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := UpdateLatestLink(base, run2); err != nil {
		t.Fatal(err)
	}
	if target, _ := os.Readlink(filepath.Join(base, "latest")); target != "2026-08-17_21-00" {
		t.Fatalf("ссылка не обновилась: %q", target)
	}
}

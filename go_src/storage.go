package main

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// SavedFile — запись об одном сохранённом файле в манифесте.
type SavedFile struct {
	Format    string   `json:"format"`
	Archive   string   `json:"archive"`
	Bytes     int      `json:"bytes"`
	SHA256    string   `json:"sha256"`
	Extracted []string `json:"extracted,omitempty"`
}

// Manifest — сводка одного запуска скачивания.
type Manifest struct {
	DownloadedAt string      `json:"downloaded_at"`
	Source       string      `json:"source"`
	Files        []SavedFile `json:"files"`
}

// RunDir возвращает путь к папке текущего запуска: <base>/2026-08-17_09-00.
func RunDir(base string, at time.Time) string {
	return filepath.Join(base, at.Format("2006-01-02_15-04"))
}

// SaveArchive кладёт архив в папку запуска под именем perechen_<format><ext>.
func SaveArchive(dir string, f Format, data []byte, ext string) (SavedFile, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return SavedFile{}, fmt.Errorf("create dir %s: %w", dir, err)
	}

	name := fmt.Sprintf("perechen_%s%s", f.Name, ext)
	path := filepath.Join(dir, name)
	if err := writeFileAtomic(path, data); err != nil {
		return SavedFile{}, err
	}

	sum := sha256.Sum256(data)
	return SavedFile{
		Format:  f.Name,
		Archive: name,
		Bytes:   len(data),
		SHA256:  hex.EncodeToString(sum[:]),
	}, nil
}

// ExtractZip распаковывает архив в ту же папку и возвращает имена файлов.
// Записи с путями наружу папки игнорируются (защита от zip slip).
func ExtractZip(dir string, data []byte) ([]string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("open zip: %w", err)
	}

	var names []string
	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() {
			continue
		}
		name := filepath.Base(filepath.Clean(strings.ReplaceAll(entry.Name, `\`, "/")))
		if name == "." || name == ".." || name == string(filepath.Separator) {
			continue
		}

		rc, err := entry.Open()
		if err != nil {
			return names, fmt.Errorf("open zip entry %q: %w", entry.Name, err)
		}
		// Ограничение на размер распаковки — защита от zip-бомбы.
		content, err := io.ReadAll(io.LimitReader(rc, 512<<20))
		rc.Close()
		if err != nil {
			return names, fmt.Errorf("read zip entry %q: %w", entry.Name, err)
		}
		if err := writeFileAtomic(filepath.Join(dir, name), content); err != nil {
			return names, err
		}
		names = append(names, name)
	}
	return names, nil
}

// WriteManifest сохраняет manifest.json в папке запуска.
func WriteManifest(dir string, m Manifest) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, "manifest.json"), append(data, '\n'))
}

// UpdateLatestLink обновляет симлинк <base>/latest на папку последнего запуска.
//
// Ссылка относительная (просто имя папки), а не абсолютная: внутри контейнера
// папка видна как /data/<дата>, а на хосте — как /volume1/.../downloads/<дата>,
// и абсолютная ссылка оказалась бы битой с одной из сторон.
// На системах без симлинков (Windows без прав) ошибка не критична.
func UpdateLatestLink(base, runDir string) error {
	link := filepath.Join(base, "latest")
	_ = os.Remove(link)
	return os.Symlink(filepath.Base(runDir), link)
}

// PruneOldRuns удаляет самые старые папки запусков, оставляя keep штук.
// keep <= 0 отключает очистку.
func PruneOldRuns(base string, keep int) ([]string, error) {
	if keep <= 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil, err
	}

	var runs []string
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "latest" {
			continue
		}
		if _, err := time.Parse("2006-01-02_15-04", e.Name()); err != nil {
			continue
		}
		runs = append(runs, e.Name())
	}
	if len(runs) <= keep {
		return nil, nil
	}

	// Имена в формате 2006-01-02_15-04 сортируются лексикографически по времени.
	sort.Strings(runs)
	var removed []string
	for _, name := range runs[:len(runs)-keep] {
		if err := os.RemoveAll(filepath.Join(base, name)); err != nil {
			return removed, err
		}
		removed = append(removed, name)
	}
	return removed, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("rename %s: %w", path, err)
	}
	return nil
}

package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const uploadPrefix = ".sshvnc-upload-"

type FileEntry struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
	Dir      bool   `json:"dir"`
	Regular  bool   `json:"regular"`
}

func remotePath(name string) (string, error) {
	if len(name) > 4096 || !strings.HasPrefix(name, "/") || strings.ContainsAny(name, "\\:\x00") || path.Clean(name) != name {
		return "", errors.New("远程路径无效")
	}
	return name, nil
}

func fileName(name string) error {
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\:\x00") ||
		strings.HasSuffix(name, " ") || strings.HasSuffix(name, ".") || strings.HasPrefix(name, uploadPrefix) {
		return errors.New("文件名无效")
	}
	return nil
}

func (s *session) list(directory string) ([]FileEntry, error) {
	directory, err := remotePath(directory)
	if err != nil {
		return nil, err
	}
	entries, err := s.files.ReadDir(directory)
	if err != nil {
		return nil, err
	}
	result := make([]FileEntry, 0, len(entries))
	for _, info := range entries {
		if strings.HasPrefix(info.Name(), uploadPrefix) {
			continue
		}
		result = append(result, FileEntry{
			Name: info.Name(), Size: info.Size(), Modified: info.ModTime().Unix(),
			Dir: info.IsDir(), Regular: info.Mode().IsRegular(),
		})
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].Dir != result[j].Dir {
			return result[i].Dir
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func (s *session) upload(local, directory string, report func(Progress)) error {
	name := filepath.Base(local)
	if err := fileName(name); err != nil {
		return err
	}
	if !filepath.IsAbs(local) {
		return errors.New("本地文件路径无效")
	}
	candidate, err := os.Lstat(local)
	if err != nil {
		return err
	}
	if !candidate.Mode().IsRegular() {
		return errors.New("请选择普通文件")
	}
	input, err := os.Open(local)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("请选择普通文件，目录上传暂不支持")
	}
	target := path.Join(directory, name)
	if _, err := s.files.Lstat(target); !os.IsNotExist(err) {
		if err == nil {
			return errors.New("远程目录已有同名文件")
		}
		return err
	}
	temp := path.Join(directory, uploadPrefix+rand.Text())
	output, err := s.files.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			s.files.Remove(temp)
		}
	}()
	err = copyFile(output, input, Progress{Name: name, Total: info.Size()}, report)
	if err := errors.Join(err, output.Close()); err != nil {
		return err
	}
	if err := s.files.Rename(temp, target); err != nil {
		return err
	}
	committed = true
	return nil
}

func (s *session) download(remote, local string, report func(Progress)) error {
	input, err := s.files.Open(remote)
	if err != nil {
		return err
	}
	defer input.Close()
	info, err := input.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("请选择普通文件")
	}
	output, err := os.CreateTemp(filepath.Dir(local), ".sshvnc-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	err = copyFile(output, input, Progress{Name: path.Base(remote), Total: info.Size()}, report)
	if err := errors.Join(err, output.Close()); err != nil {
		return err
	}
	return os.Rename(output.Name(), local)
}

type Progress struct {
	Name  string `json:"name"`
	Done  int64  `json:"done"`
	Total int64  `json:"total"`
}

type progressWriter struct {
	io.Writer
	Progress
	report func(Progress)
	last   time.Time
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.Done += int64(n)
	if time.Since(w.last) >= 100*time.Millisecond {
		w.report(w.Progress)
		w.last = time.Now()
	}
	return n, err
}

func copyFile(dst io.Writer, src io.Reader, progress Progress, report func(Progress)) error {
	report(progress)
	w := &progressWriter{Writer: dst, Progress: progress, report: report, last: time.Now()}
	_, err := io.Copy(w, src)
	if err == nil && w.Done != progress.Total {
		err = fmt.Errorf("传输期间文件大小改变了（已传输 %d / %d 字节）", w.Done, progress.Total)
	}
	report(w.Progress)
	return err
}

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type faultAppendFile struct {
	*os.File
	write func([]byte) (int, error)
	sync  func() error
}

func (f *faultAppendFile) Write(payload []byte) (int, error) {
	if f.write != nil {
		return f.write(payload)
	}
	return f.File.Write(payload)
}

func (f *faultAppendFile) Sync() error {
	if f.sync != nil {
		return f.sync()
	}
	return f.File.Sync()
}

func validCrashRequest(eventID string) crashRequest {
	return crashRequest{
		EventID: eventID, AppVersion: "1.2.3", Platform: "linux", Arch: "amd64", ReasonCode: "segfault",
		Symbols: []string{"game::update", "engine_main"},
	}
}

func TestReportStorePersistsDeduplicatesAndSecuresFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "reports")
	store, err := newReportStore(dir)
	if err != nil {
		t.Fatalf("newReportStore() error = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("store directory mode = %v, err = %v, want 0700", info.Mode().Perm(), err)
	}
	for _, collection := range storeCollections {
		info, err := os.Stat(store.collectionPath(collection))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s file mode = %v, err = %v, want 0600", collection, info.Mode().Perm(), err)
		}
	}

	request := validCrashRequest("random-event-id-0001")
	id, duplicate, err := store.appendCrash(request)
	if err != nil || duplicate || !reportIDPattern.MatchString(id) {
		t.Fatalf("appendCrash() = %q, %v, %v", id, duplicate, err)
	}
	duplicateID, duplicate, err := store.appendCrash(request)
	if err != nil || !duplicate || duplicateID != id {
		t.Fatalf("duplicate appendCrash() = %q, %v, %v, want %q, true, nil", duplicateID, duplicate, err, id)
	}

	reopened, err := newReportStore(dir)
	if err != nil {
		t.Fatalf("reopen report store: %v", err)
	}
	persistedID, duplicate, err := reopened.appendCrash(request)
	if err != nil || duplicate || persistedID == id {
		t.Fatalf("persisted dedupe = %q, %v, %v, want a new ID after restart", persistedID, duplicate, err)
	}
	records, err := reopened.crashes()
	if err != nil || len(records) != 2 || records[0].ID != id || records[1].ID != persistedID {
		t.Fatalf("persisted records = %#v, %v", records, err)
	}
	if !records[0].ServerTime.Equal(records[0].ServerTime.Truncate(storeTimePrecision)) {
		t.Fatalf("stored time %v was not rounded to %v", records[0].ServerTime, storeTimePrecision)
	}
}

func TestReportStoreSkipsMalformedTail(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.appendCrash(validCrashRequest("random-event-id-0002")); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(store.collectionPath(crashCollection), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"id":"incomplete"` + strings.Repeat("x", maxStoreLineBytes)); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := newReportStore(store.root)
	if err != nil {
		t.Fatalf("malformed tail prevented reopen: %v", err)
	}
	records, err := reopened.crashes()
	if err != nil || len(records) != 1 || len(records[0].EventID) != 64 {
		t.Fatalf("records after malformed tail = %#v, %v", records, err)
	}
}

func TestReportStoreBoundsRecordCount(t *testing.T) {
	dir := t.TempDir()
	store, err := newReportStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	now := roundedStoreTime(time.Now())
	file, err := os.OpenFile(store.collectionPath(crashCollection), os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(file)
	for i := range maxStoreRecords {
		record := crashRecord{
			ID: fmt.Sprintf("cr-%016x", i), ServerTime: now, AppVersion: "1.0",
			EventID: fmt.Sprintf("random-event-%06d", i), Platform: "linux", Arch: "amd64", ReasonCode: "test",
		}
		if err := encoder.Encode(record); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = newReportStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.appendCrash(validCrashRequest("random-event-newest")); err != nil {
		t.Fatal(err)
	}
	records, err := store.crashes()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != maxStoreRecords {
		t.Fatalf("record count = %d, want capped %d", len(records), maxStoreRecords)
	}
	if records[0].EventID == "random-event-000000" || records[len(records)-1].EventID != "random-event-newest" {
		t.Fatalf("record cap did not retain latest records: first=%q last=%q", records[0].EventID, records[len(records)-1].EventID)
	}
}

func TestReportStoreCompactionAppliesRetention(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(storeTimePrecision)
	old := crashRecord{ID: "cr-0000000000000001", ServerTime: now.Add(-crashRetention - time.Hour), AppVersion: "1.0", EventID: "random-event-id-old1", Platform: "linux", Arch: "amd64", ReasonCode: "old"}
	current := crashRecord{ID: "cr-0000000000000002", ServerTime: now, AppVersion: "1.0", EventID: "random-event-id-new1", Platform: "linux", Arch: "amd64", ReasonCode: "new"}
	contents := strings.Join([]string{
		`{"id":"` + old.ID + `","server_time":"` + old.ServerTime.Format(time.RFC3339) + `","app_version":"1.0","event_id":"` + old.EventID + `","platform":"linux","arch":"amd64","reason_code":"old"}`,
		`not-json`,
		`{"id":"` + current.ID + `","server_time":"` + current.ServerTime.Format(time.RFC3339) + `","app_version":"1.0","event_id":"` + current.EventID + `","platform":"linux","arch":"amd64","reason_code":"new"}`,
	}, "\n") + "\n"
	if err := os.WriteFile(store.collectionPath(crashCollection), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.Compact(now); err != nil {
		t.Fatalf("Compact() error = %v", err)
	}
	records, err := store.crashes()
	if err != nil || len(records) != 1 || records[0].ID != current.ID {
		t.Fatalf("compacted records = %#v, %v", records, err)
	}
	data, err := os.ReadFile(store.collectionPath(crashCollection))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), old.ID) || strings.Contains(string(data), "not-json") {
		t.Fatalf("compacted file retained expired/malformed data: %s", data)
	}
}

func TestMaintenanceCompactionRemovesExpiredDiskRecords(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	now := roundedStoreTime(time.Now())
	expired := crashRecord{
		ID: "cr-0000000000000003", ServerTime: now.Add(-crashRetention - time.Hour), AppVersion: "1.0",
		EventID: "random-event-expired", Platform: "linux", Arch: "amd64", ReasonCode: "expired",
	}
	file, err := os.OpenFile(store.collectionPath(crashCollection), os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(file).Encode(expired); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	handler := newTestLobbyHandler()
	handler.Store = store
	handler.LastStoreCompaction = now.Add(-storeCompactionInterval + time.Hour)
	handler.maintainAt(now)
	data, err := os.ReadFile(store.collectionPath(crashCollection))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), expired.ID) {
		t.Fatal("maintenance compacted before the daily interval elapsed")
	}

	handler.LastStoreCompaction = now.Add(-storeCompactionInterval)
	handler.maintainAt(now)
	data, err = os.ReadFile(store.collectionPath(crashCollection))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), expired.ID) {
		t.Fatalf("daily maintenance retained expired disk record: %s", data)
	}
}

func TestMaintenanceCompactionFailureRetriesNextTick(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := newTestLobbyHandler()
	handler.Store = store
	now := roundedStoreTime(time.Now())
	previous := now.Add(-storeCompactionInterval)
	handler.LastStoreCompaction = previous
	missingPath := store.collectionPath(performanceCollection)
	if err := os.Remove(missingPath); err != nil {
		t.Fatal(err)
	}
	handler.maintainAt(now)
	if !handler.LastStoreCompaction.Equal(previous) {
		t.Fatalf("failed compaction advanced timestamp to %v, want %v", handler.LastStoreCompaction, previous)
	}
	file, err := os.OpenFile(missingPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	retryAt := now.Add(tickInterval)
	handler.maintainAt(retryAt)
	if !handler.LastStoreCompaction.Equal(retryAt) {
		t.Fatalf("successful retry timestamp = %v, want %v", handler.LastStoreCompaction, retryAt)
	}
}

func TestReportStoreAppendRollsBackFailures(t *testing.T) {
	tests := []struct {
		name  string
		fault func(*os.File) appendFile
	}{
		{
			name: "short write",
			fault: func(file *os.File) appendFile {
				return &faultAppendFile{File: file, write: func(payload []byte) (int, error) {
					return file.Write(payload[:len(payload)/2])
				}}
			},
		},
		{
			name: "partial write error",
			fault: func(file *os.File) appendFile {
				return &faultAppendFile{File: file, write: func(payload []byte) (int, error) {
					written, _ := file.Write(payload[:len(payload)/2])
					return written, errors.New("injected write failure")
				}}
			},
		},
		{
			name: "sync error",
			fault: func(file *os.File) appendFile {
				first := true
				return &faultAppendFile{File: file, sync: func() error {
					if first {
						first = false
						return errors.New("injected sync failure")
					}
					return file.Sync()
				}}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := newReportStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			path := store.collectionPath(crashCollection)
			if _, _, err := store.appendCrash(validCrashRequest("random-event-existing")); err != nil {
				t.Fatal(err)
			}
			before, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			openNormally := store.openAppend
			store.openAppend = func(path string) (appendFile, error) {
				file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
				if err != nil {
					return nil, err
				}
				return test.fault(file), nil
			}
			if _, _, err := store.appendCrash(validCrashRequest("random-event-failed")); err == nil {
				t.Fatal("faulted append unexpectedly succeeded")
			}
			after, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if after.Size() != before.Size() {
				t.Fatalf("file size after failed append = %d, want original %d", after.Size(), before.Size())
			}

			store.openAppend = openNormally
			if _, _, err := store.appendCrash(validCrashRequest("random-event-after-failure")); err != nil {
				t.Fatal(err)
			}
			records, err := store.crashes()
			if err != nil || len(records) != 2 || records[0].EventID != "random-event-existing" || records[1].EventID != "random-event-after-failure" {
				t.Fatalf("records after rollback and append = %#v, %v", records, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.HasSuffix(string(data), "\n") {
				t.Fatalf("record after rollback was concatenated with a fragment: %q", data)
			}
		})
	}
}

func TestNetplayPersistenceQueueIsBounded(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeStarted := make(chan struct{})
	writeRelease := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(writeRelease) })
		store.Close()
	}()
	store.openAppend = func(path string) (appendFile, error) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		return &faultAppendFile{File: file, write: func(payload []byte) (int, error) {
			startedOnce.Do(func() { close(writeStarted) })
			<-writeRelease
			return file.Write(payload)
		}}, nil
	}
	if !store.enqueueNetplay("nr-0000000000000000", "1.0", "match_runtime_error") {
		t.Fatal("initial netplay persistence was not queued")
	}
	select {
	case <-writeStarted:
	case <-time.After(time.Second):
		t.Fatal("netplay worker did not block in injected write")
	}
	for i := range netplayQueueSize {
		if !store.enqueueNetplay(fmt.Sprintf("nr-%016x", i+1), "1.0", "match_runtime_error") {
			t.Fatalf("queue rejected record %d before reaching capacity", i)
		}
	}
	if store.enqueueNetplay("nr-ffffffffffffffff", "1.0", "match_runtime_error") {
		t.Fatal("queue accepted a record beyond its fixed capacity")
	}
	releaseOnce.Do(func() { close(writeRelease) })
}

func TestNetplayPendingDuplicateReceivesBoundedRetry(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	firstWriteStarted := make(chan struct{})
	firstWriteRelease := make(chan struct{})
	attempts := 0
	store.openAppend = func(path string) (appendFile, error) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		attempts++
		if attempts == 1 {
			return &faultAppendFile{File: file, write: func([]byte) (int, error) {
				close(firstWriteStarted)
				<-firstWriteRelease
				return 0, errors.New("injected first attempt failure")
			}}, nil
		}
		return file, nil
	}
	reportID := "nr-1234567890abcdef"
	if !store.enqueueNetplay(reportID, "1.0", "match_runtime_error") {
		t.Fatal("initial report was not queued")
	}
	select {
	case <-firstWriteStarted:
	case <-time.After(time.Second):
		t.Fatal("first persistence attempt did not start")
	}
	if !store.enqueueNetplay(reportID, "1.0", "match_runtime_error") {
		t.Fatal("duplicate pending report was rejected")
	}
	close(firstWriteRelease)
	waitForTestCondition(t, func() bool {
		records, err := store.netplay()
		return err == nil && len(records) == 1 && records[0].ID == reportID
	})
	if attempts != netplayMaxAttempts {
		t.Fatalf("persistence attempts = %d, want bounded %d", attempts, netplayMaxAttempts)
	}
}

func TestReportStoreCloseDrainsNetplayQueue(t *testing.T) {
	store, err := newReportStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	firstWriteStarted := make(chan struct{})
	firstWriteRelease := make(chan struct{})
	var first sync.Once
	store.openAppend = func(path string) (appendFile, error) {
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, err
		}
		return &faultAppendFile{File: file, write: func(payload []byte) (int, error) {
			blocked := false
			first.Do(func() {
				blocked = true
				close(firstWriteStarted)
			})
			if blocked {
				<-firstWriteRelease
			}
			return file.Write(payload)
		}}, nil
	}
	if !store.enqueueNetplay("nr-0000000000000101", "1.0", "match_runtime_error") {
		t.Fatal("first report was not queued")
	}
	select {
	case <-firstWriteStarted:
	case <-time.After(time.Second):
		t.Fatal("first persistence attempt did not start")
	}
	if !store.enqueueNetplay("nr-0000000000000102", "1.0", "match_runtime_error") {
		t.Fatal("second report was not queued")
	}
	closed := make(chan struct{})
	go func() {
		store.Close()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("Close returned before queued persistence was released")
	case <-time.After(25 * time.Millisecond):
	}
	close(firstWriteRelease)
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not finish after draining queued reports")
	}
	records, err := store.netplay()
	if err != nil || len(records) != 2 {
		t.Fatalf("drained netplay records = %#v, %v", records, err)
	}
}

var _ appendFile = (*faultAppendFile)(nil)

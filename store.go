package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	storeTimePrecision = 15 * time.Minute
	maxStoreFileBytes  = 64 << 20
	maxStoreReadBytes  = maxStoreFileBytes
	maxStoreLineBytes  = 16 << 10
	maxStoreRecords    = 10000
	netplayQueueSize   = 64
	netplayMaxAttempts = 2
	netplayRetryDelay  = 10 * time.Millisecond
)

const (
	crashRetention    = 90 * 24 * time.Hour
	feedbackRetention = 365 * 24 * time.Hour
	metricsRetention  = 30 * 24 * time.Hour
	netplayRetention  = 90 * 24 * time.Hour
)

var errStoreFull = errors.New("report store is full")
var errCollectionDisabled = errors.New("report collection is disabled")

type storeCollection string

const (
	crashCollection       storeCollection = "crash"
	feedbackCollection    storeCollection = "feedback"
	gameplayCollection    storeCollection = "gameplay"
	performanceCollection storeCollection = "performance"
	netplayCollection     storeCollection = "netplay"
)

var storeCollections = []storeCollection{
	crashCollection,
	feedbackCollection,
	gameplayCollection,
	performanceCollection,
	netplayCollection,
}

func reportCollections(features FeatureConfig) []storeCollection {
	collections := make([]storeCollection, 0, len(storeCollections))
	if features.CrashReports {
		collections = append(collections, crashCollection)
	}
	if features.FeedbackReports {
		collections = append(collections, feedbackCollection)
	}
	if features.GameplayMetrics {
		collections = append(collections, gameplayCollection)
	}
	if features.PerformanceMetrics {
		collections = append(collections, performanceCollection)
	}
	if features.MatchmakingReports {
		collections = append(collections, netplayCollection)
	}
	return collections
}

func collectionRetention(collection storeCollection) time.Duration {
	switch collection {
	case crashCollection:
		return crashRetention
	case feedbackCollection:
		return feedbackRetention
	case gameplayCollection, performanceCollection:
		return metricsRetention
	case netplayCollection:
		return netplayRetention
	default:
		return 0
	}
}

type crashRecord struct {
	ID         string    `json:"id"`
	ServerTime time.Time `json:"server_time"`
	AppVersion string    `json:"app_version"`
	EventID    string    `json:"event_id"`
	Platform   string    `json:"platform"`
	Arch       string    `json:"arch"`
	ReasonCode string    `json:"reason_code"`
	Symbols    []string  `json:"symbols,omitempty"`
}

type feedbackRecord struct {
	ID              string    `json:"id"`
	ServerTime      time.Time `json:"server_time"`
	AppVersion      string    `json:"app_version"`
	EventID         string    `json:"event_id"`
	Category        string    `json:"category"`
	Subject         string    `json:"subject"`
	Body            string    `json:"body"`
	RelatedReportID string    `json:"related_report_id,omitempty"`
}

type gameplayRecord struct {
	ID                string    `json:"id"`
	ServerTime        time.Time `json:"server_time"`
	AppVersion        string    `json:"app_version"`
	EventID           string    `json:"event_id"`
	Mode              string    `json:"mode"`
	Stage             string    `json:"stage"`
	Character         string    `json:"character"`
	OpponentCharacter string    `json:"opponent_character"`
	Online            bool      `json:"online"`
	Completed         bool      `json:"completed"`
	DurationFrames    int       `json:"duration_frames"`
	LocalPlayers      int       `json:"local_players"`
	CPUPlayers        int       `json:"cpu_players"`
	Result            string    `json:"result"`
}

type performanceRecord struct {
	ID               string    `json:"id"`
	ServerTime       time.Time `json:"server_time"`
	AppVersion       string    `json:"app_version"`
	EventID          string    `json:"event_id"`
	Platform         string    `json:"platform"`
	Arch             string    `json:"arch"`
	RendererFamily   string    `json:"renderer_family"`
	GPUVendor        string    `json:"gpu_vendor"`
	MemoryGiBBucket  string    `json:"memory_gib_bucket"`
	CPUCoresBucket   string    `json:"cpu_cores_bucket"`
	ResolutionBucket string    `json:"resolution_bucket"`
	SampleFrames     int       `json:"sample_frames"`
	FrameMsAvg       float64   `json:"frame_ms_avg"`
	FrameMsP95       float64   `json:"frame_ms_p95"`
}

type netplayRecord struct {
	ID         string    `json:"id"`
	ServerTime time.Time `json:"server_time"`
	AppVersion string    `json:"app_version"`
	Event      string    `json:"event"`
}

type netplayJob struct {
	record netplayRecord
}

type dedupeEntry struct {
	ID   string
	Time time.Time
}

type appendFile interface {
	Write([]byte) (int, error)
	Sync() error
	Truncate(int64) error
	Close() error
}

type reportStore struct {
	root           string
	collections    []storeCollection
	enabled        map[storeCollection]bool
	mu             sync.Mutex
	dedupe         map[storeCollection]map[string]dedupeEntry
	counts         map[storeCollection]int
	netplayMu      sync.Mutex
	netplayIDs     map[string]struct{}
	netplayPending map[string]struct{}
	netplayQueue   chan netplayJob
	netplayClosed  bool
	netplayWorker  sync.Once
	netplayWG      sync.WaitGroup
	closeOnce      sync.Once
	openAppend     func(string) (appendFile, error)
	eventDigestKey [32]byte
	eventDigests   map[storeCollection]map[string]string
	now            func() time.Time
}

func newReportStore(root string) (*reportStore, error) {
	return newReportStoreForFeatures(root, DefaultConfig().Features)
}

func newReportStoreForFeatures(root string, features FeatureConfig) (*reportStore, error) {
	if root == "" {
		return nil, errors.New("report store directory is empty")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create report store: %w", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		return nil, fmt.Errorf("secure report store directory: %w", err)
	}

	store := &reportStore{
		root:           root,
		collections:    reportCollections(features),
		enabled:        make(map[storeCollection]bool),
		dedupe:         make(map[storeCollection]map[string]dedupeEntry),
		counts:         make(map[storeCollection]int),
		netplayIDs:     make(map[string]struct{}),
		netplayPending: make(map[string]struct{}),
		netplayQueue:   make(chan netplayJob, netplayQueueSize),
		openAppend: func(path string) (appendFile, error) {
			return os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
		},
		eventDigests: make(map[storeCollection]map[string]string),
		now:          time.Now,
	}
	if _, err := rand.Read(store.eventDigestKey[:]); err != nil {
		return nil, fmt.Errorf("generate event digest key: %w", err)
	}
	for _, collection := range storeCollections {
		store.dedupe[collection] = make(map[string]dedupeEntry)
		store.eventDigests[collection] = make(map[string]string)
	}
	for _, collection := range store.collections {
		store.enabled[collection] = true
		file, err := os.OpenFile(store.collectionPath(collection), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, fmt.Errorf("create %s collection: %w", collection, err)
		}
		if err := file.Chmod(0o600); err != nil {
			file.Close()
			return nil, fmt.Errorf("secure %s collection: %w", collection, err)
		}
		if err := file.Close(); err != nil {
			return nil, fmt.Errorf("close %s collection: %w", collection, err)
		}
	}
	if err := store.Compact(time.Now()); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *reportStore) collectionPath(collection storeCollection) string {
	return filepath.Join(s.root, string(collection)+".jsonl")
}

func roundedStoreTime(now time.Time) time.Time {
	return now.UTC().Truncate(storeTimePrecision)
}

func generateStoredID(prefix string) (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return prefix + "-" + hex.EncodeToString(value[:]), nil
}

func (s *reportStore) deduplicatedLocked(collection storeCollection, eventID string, now time.Time) (string, bool) {
	entry, ok := s.dedupe[collection][eventID]
	if !ok {
		return "", false
	}
	if entry.Time.Before(now.Add(-collectionRetention(collection))) {
		delete(s.dedupe[collection], eventID)
		return "", false
	}
	return entry.ID, true
}

func (s *reportStore) eventDigest(collection storeCollection, eventID string) string {
	value := sha256.Sum256(append(append([]byte(string(collection)), s.eventDigestKey[:]...), []byte(eventID)...))
	return hex.EncodeToString(value[:])
}

func (s *reportStore) recordForStorage(collection storeCollection, record any, digests map[string]string) any {
	digestEventID := func(eventID string) string {
		digest := s.eventDigest(collection, eventID)
		digests[digest] = eventID
		return digest
	}
	switch value := record.(type) {
	case crashRecord:
		value.EventID = digestEventID(value.EventID)
		return value
	case feedbackRecord:
		value.EventID = digestEventID(value.EventID)
		return value
	case gameplayRecord:
		value.EventID = digestEventID(value.EventID)
		return value
	case performanceRecord:
		value.EventID = digestEventID(value.EventID)
		return value
	default:
		return record
	}
}

func (s *reportStore) restoreEventID(collection storeCollection, eventID string) string {
	if value, ok := s.eventDigests[collection][eventID]; ok {
		return value
	}
	return eventID
}

func (s *reportStore) appendJSONLocked(collection storeCollection, record any) error {
	if !s.enabled[collection] {
		return errCollectionDisabled
	}
	digests := make(map[string]string, 1)
	line, err := json.Marshal(s.recordForStorage(collection, record, digests))
	if err != nil {
		return err
	}
	if len(line)+1 > maxStoreLineBytes {
		return errors.New("stored record is too large")
	}
	path := s.collectionPath(collection)
	if s.counts[collection] >= maxStoreRecords {
		if err := s.trimOldestLocked(collection, s.now(), maxStoreRecords-1); err != nil {
			return err
		}
	}
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if info.Size()+int64(len(line)+1) > maxStoreFileBytes {
		if err := s.compactCollectionLocked(collection, s.now()); err != nil {
			return err
		}
		info, err = os.Stat(path)
		if err != nil {
			return err
		}
		if info.Size()+int64(len(line)+1) > maxStoreFileBytes {
			return errStoreFull
		}
	}

	originalSize := info.Size()
	file, err := s.openAppend(path)
	if err != nil {
		return err
	}
	defer file.Close()
	payload := append(line, '\n')
	written, writeErr := file.Write(payload)
	if writeErr != nil || written != len(payload) {
		if writeErr == nil {
			writeErr = io.ErrShortWrite
		}
		return rollbackAppend(file, originalSize, writeErr)
	}
	if err := file.Sync(); err != nil {
		return rollbackAppend(file, originalSize, err)
	}
	for digest, eventID := range digests {
		s.eventDigests[collection][digest] = eventID
	}
	s.counts[collection]++
	return nil
}

func rollbackAppend(file appendFile, originalSize int64, cause error) error {
	truncateErr := file.Truncate(originalSize)
	syncErr := file.Sync()
	return errors.Join(cause, truncateErr, syncErr)
}

func (s *reportStore) appendCrash(request crashRequest) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := roundedStoreTime(s.now())
	if id, ok := s.deduplicatedLocked(crashCollection, request.EventID, now); ok {
		return id, true, nil
	}
	id, err := generateStoredID("cr")
	if err != nil {
		return "", false, err
	}
	record := crashRecord{
		ID: id, ServerTime: now, AppVersion: request.AppVersion, EventID: request.EventID,
		Platform: request.Platform, Arch: request.Arch, ReasonCode: request.ReasonCode,
		Symbols: append([]string(nil), request.Symbols...),
	}
	if err := s.appendJSONLocked(crashCollection, record); err != nil {
		return "", false, err
	}
	s.dedupe[crashCollection][request.EventID] = dedupeEntry{ID: id, Time: now}
	return id, false, nil
}

func (s *reportStore) appendFeedback(request feedbackRequest, version string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := roundedStoreTime(s.now())
	if id, ok := s.deduplicatedLocked(feedbackCollection, request.EventID, now); ok {
		return id, true, nil
	}
	id, err := generateStoredID("fb")
	if err != nil {
		return "", false, err
	}
	record := feedbackRecord{
		ID: id, ServerTime: now, AppVersion: version, EventID: request.EventID,
		Category: request.Category, Subject: request.Subject, Body: request.Body,
		RelatedReportID: request.RelatedReportID,
	}
	if err := s.appendJSONLocked(feedbackCollection, record); err != nil {
		return "", false, err
	}
	s.dedupe[feedbackCollection][request.EventID] = dedupeEntry{ID: id, Time: now}
	return id, false, nil
}

func (s *reportStore) appendGameplay(request gameplayRequest, version string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := roundedStoreTime(s.now())
	if _, ok := s.deduplicatedLocked(gameplayCollection, request.EventID, now); ok {
		return true, nil
	}
	id, err := generateStoredID("gm")
	if err != nil {
		return false, err
	}
	record := gameplayRecord{
		ID: id, ServerTime: now, AppVersion: version, EventID: request.EventID,
		Mode: request.Mode, Stage: request.Stage, Character: request.Character,
		OpponentCharacter: request.OpponentCharacter, Online: *request.Online,
		Completed: *request.Completed, DurationFrames: request.DurationFrames,
		LocalPlayers: request.LocalPlayers, CPUPlayers: request.CPUPlayers, Result: request.Result,
	}
	if err := s.appendJSONLocked(gameplayCollection, record); err != nil {
		return false, err
	}
	s.dedupe[gameplayCollection][request.EventID] = dedupeEntry{ID: id, Time: now}
	return false, nil
}

func (s *reportStore) appendPerformance(request performanceRequest, version string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := roundedStoreTime(s.now())
	if _, ok := s.deduplicatedLocked(performanceCollection, request.EventID, now); ok {
		return true, nil
	}
	id, err := generateStoredID("pm")
	if err != nil {
		return false, err
	}
	record := performanceRecord{
		ID: id, ServerTime: now, AppVersion: version, EventID: request.EventID,
		Platform: request.Platform, Arch: request.Arch, RendererFamily: request.RendererFamily,
		GPUVendor: request.GPUVendor, MemoryGiBBucket: request.MemoryGiBBucket,
		CPUCoresBucket: request.CPUCoresBucket, ResolutionBucket: request.ResolutionBucket,
		SampleFrames: request.SampleFrames, FrameMsAvg: request.FrameMsAvg, FrameMsP95: request.FrameMsP95,
	}
	if err := s.appendJSONLocked(performanceCollection, record); err != nil {
		return false, err
	}
	s.dedupe[performanceCollection][request.EventID] = dedupeEntry{ID: id, Time: now}
	return false, nil
}

func (s *reportStore) appendNetplay(record netplayRecord) error {
	s.netplayMu.Lock()
	_, exists := s.netplayIDs[record.ID]
	s.netplayMu.Unlock()
	if exists {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.netplayMu.Lock()
	_, exists = s.netplayIDs[record.ID]
	s.netplayMu.Unlock()
	if exists {
		return nil
	}
	if record.ServerTime.IsZero() {
		record.ServerTime = roundedStoreTime(s.now())
	}
	if err := s.appendJSONLocked(netplayCollection, record); err != nil {
		return err
	}
	s.netplayMu.Lock()
	s.netplayIDs[record.ID] = struct{}{}
	s.netplayMu.Unlock()
	return nil
}

func (s *reportStore) enqueueNetplay(reportID, version, event string) bool {
	record := netplayRecord{ID: reportID, AppVersion: version, Event: event}
	s.netplayMu.Lock()
	defer s.netplayMu.Unlock()
	if _, ok := s.netplayIDs[reportID]; ok {
		return true
	}
	if _, ok := s.netplayPending[reportID]; ok {
		return true
	}
	if s.netplayClosed {
		return false
	}
	s.netplayPending[reportID] = struct{}{}
	s.startNetplayWorker()

	select {
	case s.netplayQueue <- netplayJob{record: record}:
		return true
	default:
	}
	delete(s.netplayPending, reportID)
	return false
}

func (s *reportStore) startNetplayWorker() {
	s.netplayWorker.Do(func() {
		s.netplayWG.Go(func() {
			for job := range s.netplayQueue {
				var err error
				for attempt := range netplayMaxAttempts {
					if attempt > 0 {
						time.Sleep(netplayRetryDelay)
					}
					err = s.appendNetplay(job.record)
					if err == nil {
						break
					}
				}
				s.netplayMu.Lock()
				delete(s.netplayPending, job.record.ID)
				s.netplayMu.Unlock()
				if err != nil {
					slog.Error("Netplay report storage failed", "error", err, "reportID", job.record.ID)
				}
			}
		})
	})
}

func (s *reportStore) Close() {
	s.closeOnce.Do(func() {
		s.netplayMu.Lock()
		s.netplayClosed = true
		close(s.netplayQueue)
		s.netplayMu.Unlock()
		s.netplayWG.Wait()
	})
}

func readRecordsLocked[T any](s *reportStore, collection storeCollection, recordTime func(T) time.Time, now time.Time) ([]T, error) {
	records := make([]T, 0)
	err := scanRecordsLocked(s, collection, recordTime, now, func(record T) {
		records = append(records, record)
		if len(records) > maxStoreRecords*2 {
			records = append([]T(nil), records[len(records)-maxStoreRecords:]...)
		}
	})
	if err != nil {
		return nil, err
	}
	if len(records) > maxStoreRecords {
		records = records[len(records)-maxStoreRecords:]
	}
	return records, nil
}

func countRecordsLocked[T any](s *reportStore, collection storeCollection, recordTime func(T) time.Time, now time.Time) (int, error) {
	count := 0
	err := scanRecordsLocked(s, collection, recordTime, now, func(T) {
		if count < maxStoreRecords {
			count++
		}
	})
	return count, err
}

func scanRecordsLocked[T any](s *reportStore, collection storeCollection, recordTime func(T) time.Time, now time.Time, visit func(T)) error {
	if !s.enabled[collection] {
		return errCollectionDisabled
	}
	file, err := os.Open(s.collectionPath(collection))
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	offset := int64(0)
	if info.Size() > maxStoreReadBytes {
		offset = info.Size() - maxStoreReadBytes
		if _, err := file.Seek(offset, io.SeekStart); err != nil {
			return err
		}
	}

	reader := bufio.NewReaderSize(io.LimitReader(file, maxStoreReadBytes), maxStoreLineBytes)
	if offset > 0 {
		_, _, _ = readStoreLine(reader) // The bounded read may begin in the middle of a record.
	}
	cutoff := now.Add(-collectionRetention(collection))
	for {
		line, oversized, readErr := readStoreLine(reader)
		if readErr != nil && readErr != io.EOF {
			return readErr
		}
		var record T
		if !oversized && len(line) > 0 && json.Unmarshal(line, &record) == nil {
			switch value := any(&record).(type) {
			case *crashRecord:
				value.EventID = s.restoreEventID(collection, value.EventID)
			case *feedbackRecord:
				value.EventID = s.restoreEventID(collection, value.EventID)
			case *gameplayRecord:
				value.EventID = s.restoreEventID(collection, value.EventID)
			case *performanceRecord:
				value.EventID = s.restoreEventID(collection, value.EventID)
			}
			recordedAt := recordTime(record)
			if !recordedAt.IsZero() && !recordedAt.Before(cutoff) && !recordedAt.After(now.Add(storeTimePrecision)) {
				visit(record)
			}
		}
		if readErr == io.EOF {
			break
		}
	}
	return nil
}

func readStoreLine(reader *bufio.Reader) ([]byte, bool, error) {
	line := make([]byte, 0, 1024)
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if !oversized {
			if len(line)+len(fragment) > maxStoreLineBytes {
				line = nil
				oversized = true
			} else {
				line = append(line, fragment...)
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		return line, oversized, err
	}
}

func writeCompactedLocked[T any](s *reportStore, collection storeCollection, records []T) error {
	path := s.collectionPath(collection)
	temp, err := os.OpenFile(path+".tmp", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		temp.Close()
		if !ok {
			os.Remove(temp.Name())
		}
	}()
	writer := bufio.NewWriter(temp)
	encoder := json.NewEncoder(writer)
	digests := make(map[string]string, len(records))
	for _, record := range records {
		if err := encoder.Encode(s.recordForStorage(collection, record, digests)); err != nil {
			return err
		}
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := os.Rename(temp.Name(), path); err != nil {
		return err
	}
	s.eventDigests[collection] = digests
	if err := os.Chmod(path, 0o600); err != nil {
		return err
	}
	if err := syncParentDirectory(path); err != nil {
		return err
	}
	ok = true
	return nil
}

func syncParentDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (s *reportStore) compactCollectionLocked(collection storeCollection, now time.Time) error {
	switch collection {
	case crashCollection:
		records, err := readRecordsLocked(s, collection, func(record crashRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		return writeCompactedLocked(s, collection, records)
	case feedbackCollection:
		records, err := readRecordsLocked(s, collection, func(record feedbackRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		return writeCompactedLocked(s, collection, records)
	case gameplayCollection:
		records, err := readRecordsLocked(s, collection, func(record gameplayRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		return writeCompactedLocked(s, collection, records)
	case performanceCollection:
		records, err := readRecordsLocked(s, collection, func(record performanceRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		return writeCompactedLocked(s, collection, records)
	case netplayCollection:
		records, err := readRecordsLocked(s, collection, func(record netplayRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		return writeCompactedLocked(s, collection, records)
	default:
		return errors.New("unknown report collection")
	}
}

func (s *reportStore) trimOldestLocked(collection storeCollection, now time.Time, limit int) error {
	switch collection {
	case crashCollection:
		records, err := readRecordsLocked(s, collection, func(record crashRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		records = retainLatest(records, limit)
		if err := writeCompactedLocked(s, collection, records); err != nil {
			return err
		}
		clear(s.dedupe[collection])
		for _, record := range records {
			s.dedupe[collection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[collection] = len(records)
		return nil
	case feedbackCollection:
		records, err := readRecordsLocked(s, collection, func(record feedbackRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		records = retainLatest(records, limit)
		if err := writeCompactedLocked(s, collection, records); err != nil {
			return err
		}
		clear(s.dedupe[collection])
		for _, record := range records {
			s.dedupe[collection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[collection] = len(records)
		return nil
	case gameplayCollection:
		records, err := readRecordsLocked(s, collection, func(record gameplayRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		records = retainLatest(records, limit)
		if err := writeCompactedLocked(s, collection, records); err != nil {
			return err
		}
		clear(s.dedupe[collection])
		for _, record := range records {
			s.dedupe[collection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[collection] = len(records)
		return nil
	case performanceCollection:
		records, err := readRecordsLocked(s, collection, func(record performanceRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		records = retainLatest(records, limit)
		if err := writeCompactedLocked(s, collection, records); err != nil {
			return err
		}
		clear(s.dedupe[collection])
		for _, record := range records {
			s.dedupe[collection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[collection] = len(records)
		return nil
	case netplayCollection:
		records, err := readRecordsLocked(s, collection, func(record netplayRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		records = retainLatest(records, limit)
		if err := writeCompactedLocked(s, collection, records); err != nil {
			return err
		}
		s.netplayMu.Lock()
		clear(s.netplayIDs)
		for _, record := range records {
			s.netplayIDs[record.ID] = struct{}{}
		}
		s.netplayMu.Unlock()
		s.counts[collection] = len(records)
		return nil
	default:
		return errors.New("unknown report collection")
	}
}

func retainLatest[T any](records []T, limit int) []T {
	if len(records) <= limit {
		return records
	}
	return records[len(records)-limit:]
}

func (s *reportStore) Compact(now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, collection := range s.collections {
		if err := s.compactCollectionLocked(collection, now); err != nil {
			return fmt.Errorf("compact %s collection: %w", collection, err)
		}
	}
	return s.rebuildDedupeLocked(now)
}

func (s *reportStore) rebuildDedupeLocked(now time.Time) error {
	for _, collection := range s.collections {
		clear(s.dedupe[collection])
		s.counts[collection] = 0
	}
	s.netplayMu.Lock()
	clear(s.netplayIDs)
	s.netplayMu.Unlock()
	if s.enabled[crashCollection] {
		crashes, err := readRecordsLocked(s, crashCollection, func(record crashRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		for _, record := range crashes {
			s.dedupe[crashCollection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[crashCollection] = len(crashes)
	}
	if s.enabled[feedbackCollection] {
		feedback, err := readRecordsLocked(s, feedbackCollection, func(record feedbackRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		for _, record := range feedback {
			s.dedupe[feedbackCollection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[feedbackCollection] = len(feedback)
	}
	if s.enabled[gameplayCollection] {
		gameplay, err := readRecordsLocked(s, gameplayCollection, func(record gameplayRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		for _, record := range gameplay {
			s.dedupe[gameplayCollection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[gameplayCollection] = len(gameplay)
	}
	if s.enabled[performanceCollection] {
		performance, err := readRecordsLocked(s, performanceCollection, func(record performanceRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		for _, record := range performance {
			s.dedupe[performanceCollection][record.EventID] = dedupeEntry{ID: record.ID, Time: record.ServerTime}
		}
		s.counts[performanceCollection] = len(performance)
	}
	if s.enabled[netplayCollection] {
		netplay, err := readRecordsLocked(s, netplayCollection, func(record netplayRecord) time.Time { return record.ServerTime }, now)
		if err != nil {
			return err
		}
		s.counts[netplayCollection] = len(netplay)
		s.netplayMu.Lock()
		for _, record := range netplay {
			s.netplayIDs[record.ID] = struct{}{}
		}
		s.netplayMu.Unlock()
	}
	return nil
}

func (s *reportStore) crashes() ([]crashRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRecordsLocked(s, crashCollection, func(record crashRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) feedback() ([]feedbackRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRecordsLocked(s, feedbackCollection, func(record feedbackRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) gameplay() ([]gameplayRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRecordsLocked(s, gameplayCollection, func(record gameplayRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) performance() ([]performanceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRecordsLocked(s, performanceCollection, func(record performanceRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) netplay() ([]netplayRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return readRecordsLocked(s, netplayCollection, func(record netplayRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) crashCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return countRecordsLocked(s, crashCollection, func(record crashRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) feedbackCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return countRecordsLocked(s, feedbackCollection, func(record feedbackRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) gameplayCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return countRecordsLocked(s, gameplayCollection, func(record gameplayRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) performanceCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return countRecordsLocked(s, performanceCollection, func(record performanceRecord) time.Time { return record.ServerTime }, s.now())
}

func (s *reportStore) netplayCount() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return countRecordsLocked(s, netplayCollection, func(record netplayRecord) time.Time { return record.ServerTime }, s.now())
}

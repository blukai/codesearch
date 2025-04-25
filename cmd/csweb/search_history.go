package main

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type SearchHistoryEntry struct {
	CreatedAt time.Time
	Pattern   string
}

type SearchHistoryPatternAggregate struct {
	MaxCreatedAt time.Time
	Count        uint
	Pattern      string
}

type SearchHistoryStorer interface {
	PatternAggregates() map[string]*SearchHistoryPatternAggregate
	Append(pattern string) error
}

type fileSearchHistoryStore struct {
	file              string
	patternAggregates map[string]*SearchHistoryPatternAggregate
}

func fileSearchHistoryEntryToString(entry *SearchHistoryEntry) string {
	return fmt.Sprintf("%d %s", entry.CreatedAt.Unix(), entry.Pattern)
}

func fileSearchHistoryEntryFromString(s string) (*SearchHistoryEntry, error) {
	parts := strings.SplitN(s, " ", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("corrupt entry: %q", s)
	}

	entry := SearchHistoryEntry{Pattern: parts[1]}

	createdAtTimestamp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp %q: %w", parts[0], err)
	}
	entry.CreatedAt = time.Unix(createdAtTimestamp, 0)

	return &entry, nil
}

func (s *fileSearchHistoryStore) upsertAggregate(entry *SearchHistoryEntry) {
	aggregate, ok := s.patternAggregates[entry.Pattern]
	if !ok {
		aggregate = &SearchHistoryPatternAggregate{
			Pattern: entry.Pattern,
		}
		s.patternAggregates[entry.Pattern] = aggregate
	}
	assert(entry.CreatedAt.After(aggregate.MaxCreatedAt))
	aggregate.MaxCreatedAt = entry.CreatedAt
	aggregate.Count += 1
}

func (s *fileSearchHistoryStore) PatternAggregates() map[string]*SearchHistoryPatternAggregate {
	return s.patternAggregates
}

func (s *fileSearchHistoryStore) Append(pattern string) error {
	entry := &SearchHistoryEntry{CreatedAt: time.Now(), Pattern: pattern}
	s.upsertAggregate(entry)

	f, err := os.OpenFile(s.file, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("could not open file %q: %w", s.file, err)
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%s\n", fileSearchHistoryEntryToString(entry))
	if err != nil {
		return fmt.Errorf("could not write: %w", err)
	}

	return nil
}

func NewFileSearchHistoryStore(file string) (SearchHistoryStorer, error) {
	f, err := os.Open(file)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("could not open file: %w", err)
	}
	defer f.Close()

	store := fileSearchHistoryStore{
		file:              file,
		patternAggregates: make(map[string]*SearchHistoryPatternAggregate, 1024),
	}

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		entry, err := fileSearchHistoryEntryFromString(scanner.Text())
		if err != nil {
			return nil, err
		}
		store.upsertAggregate(entry)
	}

	return &store, nil
}

package git

import (
	"context"
	"sync"
)

// seams are the points inside a call where a test acts, as a session's push
// or a crash would at that moment. Each runs once and only when a test set
// it; production never does.
type seams struct {
	mu sync.Mutex
	// betweenReads runs between the two reads of a head observation.
	betweenReads func()
	// effectLanded runs right after a head effect lands; an error makes the
	// call stop there, as if the driver died.
	effectLanded func() error
	// exportStaged runs once an export has staged its work in its
	// directory, with the function that ends the export's deadline.
	exportStaged func(cut context.CancelFunc)
	// repositoryMoved runs right after a deletion moved the repository away
	// from its address; an error makes the call stop there, as if the
	// driver died.
	repositoryMoved func() error
}

// takeBetweenReads returns the between-reads seam, once.
func (s *seams) takeBetweenReads() func() {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.betweenReads
	s.betweenReads = nil
	return f
}

// takeEffectLanded returns the effect-landed seam, once.
func (s *seams) takeEffectLanded() func() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.effectLanded
	s.effectLanded = nil
	return f
}

// takeExportStaged returns the export-staged seam, once.
func (s *seams) takeExportStaged() func(context.CancelFunc) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.exportStaged
	s.exportStaged = nil
	return f
}

// takeRepositoryMoved returns the repository-moved seam, once.
func (s *seams) takeRepositoryMoved() func() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.repositoryMoved
	s.repositoryMoved = nil
	return f
}

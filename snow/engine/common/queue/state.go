// Copyright (C) 2019-2021, Ava Labs, Inc. All rights reserved.
// See the file LICENSE for licensing terms.

package queue

import (
	"fmt"

	"github.com/Toinounet21/avalanchego-mod/cache"
	"github.com/Toinounet21/avalanchego-mod/cache/metercacher"
	"github.com/Toinounet21/avalanchego-mod/database"
	"github.com/Toinounet21/avalanchego-mod/database/linkeddb"
	"github.com/Toinounet21/avalanchego-mod/database/prefixdb"
	"github.com/Toinounet21/avalanchego-mod/ids"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	dependentsCacheSize = 1024
	jobsCacheSize       = 2048
)

var (
	runnableJobIDsKey = []byte("runnable")
	jobsKey           = []byte("jobs")
	dependenciesKey   = []byte("dependencies")
	missingJobIDsKey  = []byte("missing job IDs")
	pendingJobsKey    = []byte("pendingJobs")
)

type state struct {
	parser         Parser
	runnableJobIDs linkeddb.LinkedDB
	cachingEnabled bool
	jobsCache      cache.Cacher
	jobs           database.Database
	// Should be prefixed with the jobID that we are attempting to find the
	// dependencies of. This prefixdb.Database should then be wrapped in a
	// linkeddb.LinkedDB to read the dependencies.
	dependencies database.Database
	// This is a cache that tracks LinkedDB iterators that have recently been
	// made.
	dependentsCache cache.Cacher
	missingJobIDs   linkeddb.LinkedDB
	// data store that tracks the last known checkpoint of how many jobs were pending in the queue.
	pendingJobs database.KeyValueReaderWriter
	// represents the number of pending jobs in the queue.
	numPendingJobs uint64
}

func newState(
	db database.Database,
	metricsNamespace string,
	metricsRegisterer prometheus.Registerer,
) (*state, error) {
	jobsCacheMetricsNamespace := fmt.Sprintf("%s_jobs_cache", metricsNamespace)
	jobsCache, err := metercacher.New(jobsCacheMetricsNamespace, metricsRegisterer, &cache.LRU{Size: jobsCacheSize})
	if err != nil {
		return nil, fmt.Errorf("couldn't create metered cache: %w", err)
	}

	pendingJobs := prefixdb.New(pendingJobsKey, db)
	numPendingJobs, err := getPendingJobs(pendingJobs)
	if err != nil {
		return nil, fmt.Errorf("couldn't initialize pending jobs: %w", err)
	}
	return &state{
		runnableJobIDs:  linkeddb.NewDefault(prefixdb.New(runnableJobIDsKey, db)),
		cachingEnabled:  true,
		jobsCache:       jobsCache,
		jobs:            prefixdb.New(jobsKey, db),
		dependencies:    prefixdb.New(dependenciesKey, db),
		dependentsCache: &cache.LRU{Size: dependentsCacheSize},
		missingJobIDs:   linkeddb.NewDefault(prefixdb.New(missingJobIDsKey, db)),
		pendingJobs:     pendingJobs,
		numPendingJobs:  numPendingJobs,
	}, nil
}

// TODO remove this in a future release, since by then it's likely most customers will have a checkpoint set.
// This is to avoid the edge-condition where a customer may have partially bootstrapped before this release,
// and won't have a checkpoint on disk to go off of.
func initializePendingJobs(d database.Database) (uint64, error) {
	var pendingJobs uint64
	iterator := d.NewIterator()
	defer iterator.Release()

	for iterator.Next() {
		pendingJobs++
	}

	return pendingJobs, iterator.Error()
}

func getPendingJobs(d database.Database) (uint64, error) {
	pendingJobs, err := database.GetUInt64(d, pendingJobsKey)

	if err == database.ErrNotFound {
		return initializePendingJobs(d) // If we don't have a checkpoint, we need to initialize it.
	}

	return pendingJobs, err
}

// AddRunnableJob adds [jobID] to the runnable queue
func (s *state) AddRunnableJob(jobID ids.ID) error {
	return s.runnableJobIDs.Put(jobID[:], nil)
}

// HasRunnableJob returns true if there is a job that can be run on the queue
func (s *state) HasRunnableJob() (bool, error) {
	isEmpty, err := s.runnableJobIDs.IsEmpty()
	return !isEmpty, err
}

// RemoveRunnableJob fetches and deletes the next job from the runnable queue
func (s *state) RemoveRunnableJob() (Job, error) {
	jobIDBytes, err := s.runnableJobIDs.HeadKey()
	if err != nil {
		return nil, err
	}
	if err := s.runnableJobIDs.Delete(jobIDBytes); err != nil {
		return nil, err
	}

	jobID, err := ids.ToID(jobIDBytes)
	if err != nil {
		return nil, fmt.Errorf("couldn't convert job ID bytes to job ID: %w", err)
	}
	job, err := s.GetJob(jobID)
	if err != nil {
		return nil, err
	}

	if err := s.jobs.Delete(jobIDBytes); err != nil {
		return job, err
	}

	// Guard rail to make sure we don't underflow.
	if s.numPendingJobs == 0 {
		return job, nil
	}
	s.numPendingJobs--

	return job, database.PutUInt64(s.pendingJobs, pendingJobsKey, s.numPendingJobs)
}

// PutJob adds the job to the queue
func (s *state) PutJob(job Job) error {
	id := job.ID()
	if s.cachingEnabled {
		s.jobsCache.Put(id, job)
	}

	if err := s.jobs.Put(id[:], job.Bytes()); err != nil {
		return err
	}

	s.numPendingJobs++
	return database.PutUInt64(s.pendingJobs, pendingJobsKey, s.numPendingJobs)
}

// HasJob returns true if the job [id] is in the queue
func (s *state) HasJob(id ids.ID) (bool, error) {
	if s.cachingEnabled {
		if _, exists := s.jobsCache.Get(id); exists {
			return true, nil
		}
	}
	return s.jobs.Has(id[:])
}

// GetJob returns the job [id]
func (s *state) GetJob(id ids.ID) (Job, error) {
	if s.cachingEnabled {
		if job, exists := s.jobsCache.Get(id); exists {
			return job.(Job), nil
		}
	}
	jobBytes, err := s.jobs.Get(id[:])
	if err != nil {
		return nil, err
	}
	job, err := s.parser.Parse(jobBytes)
	if err == nil && s.cachingEnabled {
		s.jobsCache.Put(id, job)
	}
	return job, err
}

// AddDependency adds [dependent] as blocking on [dependency] being completed
func (s *state) AddDependency(dependency, dependent ids.ID) error {
	dependentsDB := s.getDependentsDB(dependency)
	return dependentsDB.Put(dependent[:], nil)
}

// RemoveDependencies removes the set of IDs that are blocking on the completion of
// [dependency] from the database and returns them.
func (s *state) RemoveDependencies(dependency ids.ID) ([]ids.ID, error) {
	dependentsDB := s.getDependentsDB(dependency)
	iterator := dependentsDB.NewIterator()
	defer iterator.Release()

	dependents := []ids.ID(nil)
	for iterator.Next() {
		dependentKey := iterator.Key()
		if err := dependentsDB.Delete(dependentKey); err != nil {
			return nil, err
		}
		dependent, err := ids.ToID(dependentKey)
		if err != nil {
			return nil, err
		}
		dependents = append(dependents, dependent)
	}
	return dependents, iterator.Error()
}

func (s *state) DisableCaching() {
	s.dependentsCache.Flush()
	s.jobsCache.Flush()
	s.cachingEnabled = false
}

func (s *state) AddMissingJobIDs(missingIDs ids.Set) error {
	for missingID := range missingIDs {
		missingID := missingID
		if err := s.missingJobIDs.Put(missingID[:], nil); err != nil {
			return err
		}
	}
	return nil
}

func (s *state) RemoveMissingJobIDs(missingIDs ids.Set) error {
	for missingID := range missingIDs {
		missingID := missingID
		if err := s.missingJobIDs.Delete(missingID[:]); err != nil {
			return err
		}
	}
	return nil
}

func (s *state) MissingJobIDs() ([]ids.ID, error) {
	iterator := s.missingJobIDs.NewIterator()
	defer iterator.Release()

	missingIDs := []ids.ID(nil)
	for iterator.Next() {
		missingID, err := ids.ToID(iterator.Key())
		if err != nil {
			return nil, err
		}
		missingIDs = append(missingIDs, missingID)
	}
	return missingIDs, nil
}

func (s *state) getDependentsDB(dependency ids.ID) linkeddb.LinkedDB {
	if s.cachingEnabled {
		if dependentsDBIntf, ok := s.dependentsCache.Get(dependency); ok {
			return dependentsDBIntf.(linkeddb.LinkedDB)
		}
	}
	dependencyDB := prefixdb.New(dependency[:], s.dependencies)
	dependentsDB := linkeddb.NewDefault(dependencyDB)
	if s.cachingEnabled {
		s.dependentsCache.Put(dependency, dependentsDB)
	}
	return dependentsDB
}
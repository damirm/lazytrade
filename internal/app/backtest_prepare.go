package app

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/damirm/lazytrade/internal/config"
)

type preparedBacktestPaths struct {
	configDir    string
	datasetPath  string
	manifestPath string
	outputDir    string
}

type preparedBacktestRun struct {
	run             config.BacktestRun
	strategy        config.StrategyConfig
	configDir       string
	datasetPath     string
	manifestPath    string
	outputDir       string
	metadata        resolvedDatasetMetadata
	datasetSnapshot *os.File
	snapshotPath    string
	datasetChecksum string
}

func prepareBacktestRun(options BacktestOptions, run config.BacktestRun, strategy config.StrategyConfig, runCount int) (*preparedBacktestRun, error) {
	paths, err := resolvePreparedBacktestPaths(options, run, runCount)
	if err != nil {
		return nil, err
	}

	snapshot, snapshotPath, checksum, err := snapshotDataset(paths.datasetPath)
	if err != nil {
		return nil, fmt.Errorf("prepare dataset snapshot: %w", err)
	}
	prepared := &preparedBacktestRun{
		run:             run,
		strategy:        strategy,
		configDir:       paths.configDir,
		datasetPath:     paths.datasetPath,
		manifestPath:    paths.manifestPath,
		outputDir:       paths.outputDir,
		datasetSnapshot: snapshot,
		snapshotPath:    snapshotPath,
		datasetChecksum: checksum,
	}
	metadata, err := resolveDatasetMetadata(paths.datasetPath, paths.manifestPath, run, strategy)
	if err != nil {
		return nil, errors.Join(err, prepared.cleanup())
	}
	prepared.metadata = metadata
	return prepared, nil
}

func resolvePreparedBacktestPaths(options BacktestOptions, run config.BacktestRun, runCount int) (preparedBacktestPaths, error) {
	return resolvePreparedBacktestPathsWithAbs(options, run, runCount, filepath.Abs)
}

func resolvePreparedBacktestPathsWithAbs(
	options BacktestOptions,
	run config.BacktestRun,
	runCount int,
	absPath func(string) (string, error),
) (preparedBacktestPaths, error) {
	configDir, err := absPath(filepath.Dir(options.ConfigPath))
	if err != nil {
		return preparedBacktestPaths{}, fmt.Errorf("resolve config directory: %w", err)
	}
	outputDir, err := outputDirectory(configDir, options.Output, run, runCount)
	if err != nil {
		return preparedBacktestPaths{}, fmt.Errorf("resolve output directory: %w", err)
	}
	paths := preparedBacktestPaths{
		configDir:   configDir,
		datasetPath: resolvePath(configDir, run.Data.Path),
		outputDir:   outputDir,
	}
	if run.Data.MetadataPath != "" {
		paths.manifestPath = resolvePath(configDir, run.Data.MetadataPath)
	}
	return paths, nil
}

func snapshotDataset(sourcePath string) (_ *os.File, snapshotPath, checksum string, returnErr error) {
	source, err := os.Open(sourcePath)
	if err != nil {
		return nil, "", "", fmt.Errorf("open source dataset %q: %w", sourcePath, err)
	}
	defer func() {
		if source != nil {
			returnErr = errors.Join(returnErr, source.Close())
		}
	}()

	writable, err := os.CreateTemp("", "lazytrade-backtest-dataset-*")
	if err != nil {
		return nil, "", "", fmt.Errorf("create temporary dataset snapshot: %w", err)
	}
	snapshotPath = writable.Name()
	keepSnapshot := false
	defer func() {
		var closeErr error
		if writable != nil {
			closeErr = writable.Close()
		}
		if returnErr != nil || !keepSnapshot {
			removeErr := os.Remove(snapshotPath)
			if errors.Is(removeErr, os.ErrNotExist) {
				removeErr = nil
			}
			returnErr = errors.Join(returnErr, closeErr, removeErr)
		}
	}()

	digest := sha256.New()
	if _, err := io.Copy(io.MultiWriter(writable, digest), source); err != nil {
		return nil, "", "", fmt.Errorf("copy source dataset %q: %w", sourcePath, err)
	}
	if err := source.Close(); err != nil {
		source = nil
		return nil, "", "", fmt.Errorf("close source dataset %q: %w", sourcePath, err)
	}
	source = nil
	closeErr := writable.Close()
	writable = nil
	if closeErr != nil {
		return nil, "", "", fmt.Errorf("close writable dataset snapshot: %w", closeErr)
	}
	readonly, err := os.Open(snapshotPath)
	if err != nil {
		return nil, "", "", fmt.Errorf("reopen dataset snapshot: %w", err)
	}
	keepSnapshot = true
	return readonly, snapshotPath, hex.EncodeToString(digest.Sum(nil)), nil
}

func (p *preparedBacktestRun) cleanup() error {
	if p == nil {
		return nil
	}
	var closeErr error
	if p.datasetSnapshot != nil {
		closeErr = p.datasetSnapshot.Close()
		p.datasetSnapshot = nil
	}
	var removeErr error
	if p.snapshotPath != "" {
		removeErr = os.Remove(p.snapshotPath)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		p.snapshotPath = ""
	}
	return errors.Join(closeErr, removeErr)
}

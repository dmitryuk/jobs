package jobs

import (
	"context"
	"sync"
	"time"

	jobsApi "github.com/roadrunner-server/api/v4/plugins/v3/jobs"
	"go.uber.org/zap"
)

type processor struct {
	wg         sync.WaitGroup
	mu         sync.Mutex
	consumers  *sync.Map
	runners    *map[string]struct{}
	log        *zap.Logger
	queueCh    chan *pjob
	maxWorkers int
	errs       []error
}

type pjob struct {
	jc        jobsApi.Constructor
	pipe      jobsApi.Pipeline
	queue     jobsApi.Queue
	cmdCh     chan<- jobsApi.Commander
	configKey string
	timeout   int
}

func newPipesProc(log *zap.Logger, consumers *sync.Map, runners *map[string]struct{}, maxWorkers int) *processor {
	p := &processor{
		log:        log,
		queueCh:    make(chan *pjob, 100),
		maxWorkers: maxWorkers,
		consumers:  consumers,
		runners:    runners,
		wg:         sync.WaitGroup{},
		mu:         sync.Mutex{},
		errs:       make([]error, 0, 1),
	}

	// start the processor
	p.run()

	return p
}

func (p *processor) run() {
	for i := 0; i < p.maxWorkers; i++ {
		go func() {
			for job := range p.queueCh {
				p.log.Debug("initializing driver", zap.String("pipeline", job.pipe.Name()), zap.String("driver", job.pipe.Driver()))
				t := time.Now().UTC()
				initializedDriver, err := job.jc.DriverFromConfig(job.configKey, job.queue, job.pipe, job.cmdCh)
				if err != nil {
					p.mu.Lock()
					p.errs = append(p.errs, err)
					p.mu.Unlock()
					p.wg.Done()
					p.log.Error("failed to initialize driver",
						zap.String("pipeline", job.pipe.Name()),
						zap.String("driver", job.pipe.Driver()),
						zap.Error(err))
					continue
				}

				// add a driver to the set of the consumers (name - pipeline name, value - associated driver)
				p.consumers.Store(job.pipe.Name(), initializedDriver)

				p.log.Debug("driver ready", zap.String("pipeline", job.pipe.Name()), zap.String("driver", job.pipe.Driver()), zap.Time("start", t), zap.Int64("elapsed", time.Since(t).Milliseconds()))
				// if a pipeline initialized to be consumed, call Run on it
				if _, ok := (*p.runners)[job.pipe.Name()]; ok {
					ctx, cancel := context.WithTimeout(context.Background(), time.Second*time.Duration(job.timeout))
					err = initializedDriver.Run(ctx, job.pipe)
					if err != nil {
						p.mu.Lock()
						p.errs = append(p.errs, err)
						p.mu.Unlock()
					}
					cancel()
				}
				p.wg.Done()
			}

			p.log.Debug("exited from jobs pipeline processor")
		}()
	}
}

func (p *processor) add(pjob *pjob) {
	p.wg.Add(1)
	p.queueCh <- pjob
}

func (p *processor) errors() []error {
	p.mu.Lock()
	defer p.mu.Unlock()
	errs := make([]error, len(p.errs))
	copy(errs, p.errs)
	return errs
}

func (p *processor) wait() {
	p.wg.Wait()
}

func (p *processor) stop() {
	close(p.queueCh)
}

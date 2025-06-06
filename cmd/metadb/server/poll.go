package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/confluentinc/confluent-kafka-go/v2/kafka"
	"github.com/metadb-project/metadb/cmd/metadb/catalog"
	"github.com/metadb-project/metadb/cmd/metadb/change"
	"github.com/metadb-project/metadb/cmd/metadb/command"
	"github.com/metadb-project/metadb/cmd/metadb/dbx"
	"github.com/metadb-project/metadb/cmd/metadb/dsync"
	"github.com/metadb-project/metadb/cmd/metadb/log"
	"github.com/metadb-project/metadb/cmd/metadb/status"
	"github.com/metadb-project/metadb/cmd/metadb/sysdb"
	"github.com/metadb-project/metadb/cmd/metadb/util"
)

func goPollLoop(ctx context.Context, cat *catalog.Catalog, svr *server) {
	if svr.opt.NoKafkaCommit {
		log.Info("Kafka commits disabled")
	}
	var spr *sproc
	var err error
	// For now, we support only one source
	spr, err = waitForConfig(svr)
	if err != nil {
		log.Fatal("%s", err)
		os.Exit(1)
	}

	err = logSyncMode(svr.dp, spr.source.Name)
	if err != nil {
		log.Fatal("%s", err)
		os.Exit(1)
	}

	folio, err := isFolioModulePresent(svr.db)
	if err != nil {
		log.Error("checking for folio module: %v", err)
	}
	reshare, err := isReshareModulePresent(svr.db)
	if err != nil {
		log.Error("checking for reshare module: %v", err)
	}
	if !svr.opt.Script {
		go goMaintenance(svr.opt.Datadir, *(svr.db), svr.dp, cat, spr.source.Name, folio, reshare)
	}

	for {
		err := launchPollLoop(ctx, cat, svr, spr)
		if err == nil {
			break
		}
		spr.source.Status.Stream.Error()
		if !svr.opt.Script {
			time.Sleep(24 * time.Hour)
		} else {
			time.Sleep(1 * time.Hour)
		}
	}
}

func logSyncMode(dq dbx.Queryable, source string) error {
	mode, err := dsync.ReadSyncMode(dq, source)
	if err != nil {
		return fmt.Errorf("logging sync mode: %w", err)
	}
	var modestr string
	switch mode {
	case dsync.InitialSync:
		modestr = "initial"
	case dsync.Resync:
		modestr = "resync"
	default:
		return nil
	}
	log.Info("synchronizing source %q (%s)", source, modestr)
	return nil
}

func launchPollLoop(ctx context.Context, cat *catalog.Catalog, svr *server, spr *sproc) (reterr error) {
	defer func() {
		if r := recover(); r != nil {
			reterr = fmt.Errorf("%v", r)
			log.Error("%s", reterr)
			// Log stack trace.
			buf := make([]byte, 65536)
			n := runtime.Stack(buf, true)
			log.Detail("%s", buf[0:n])
		}
	}()
	reterr = outerPollLoop(ctx, cat, svr, spr)
	if reterr != nil {
		panic(reterr.Error())
	}
	return
}

func outerPollLoop(ctx context.Context, cat *catalog.Catalog, svr *server, spr *sproc) error {
	var err error
	// Set up source log
	if svr.opt.LogSource != "" {
		if spr.sourceLog, err = log.NewSourceLog(svr.opt.LogSource); err != nil {
			return err
		}
	}

	log.Debug("starting stream processor")
	if err = pollLoop(ctx, cat, spr); err != nil {
		//log.Error("%s", err)
		return err
	}
	return nil
}

func pollLoop(ctx context.Context, cat *catalog.Catalog, spr *sproc) error {
	// var database0 *sysdb.DatabaseConnector = spr.databases[0]
	//if database0.Type == "postgresql" && database0.DBPort == "" {
	//	database0.DBPort = "5432"
	//}
	//dsn := &sqlx.DSN{
	//	// DBURI: spr.svr.dburi,
	//	Host:     spr.svr.db.Host,
	//	Port:     "5432",
	//	User:     spr.svr.db.User,
	//	Password: spr.svr.db.Password,
	//	DBName:   spr.svr.db.DBName,
	//	SSLMode:  "require",
	//	// Account:  database0.DBAccount,
	//}
	//db, err := sqlx.Open("postgresql", dsn)
	//if err != nil {
	//	return err
	//}
	//// Ping database to test connection
	//if err = db.Ping(); err != nil {
	//	spr.databases[0].Status.Error()
	//	return fmt.Errorf("connecting to database: ping: %s", err)
	//}
	//////////////////////////////////////////////////////////////////////////////
	dc, err := spr.svr.db.Connect()
	if err != nil {
		return err
	}
	// dcsuper, err := spr.svr.db.ConnectSuper()
	// if err != nil {
	// 	return err
	// }
	//////////////////////////////////////////////////////////////////////////////
	//spr.db = append(spr.db, db)
	// Cache tracking
	//if err = metadata.Init(spr.svr.db, spr.svr.opt.MetadbVersion); err != nil {
	//	return err
	//}
	// Cache schema
	/*	schema, err := cache.NewSchema(db, cat)
		if err != nil {
			return fmt.Errorf("caching schema: %s", err)
		}
	*/

	// Cache users
	/*	users, err := cache.NewUsers(db)
		if err != nil {
			return fmt.Errorf("caching users: %s", err)
		}
	*/
	// Read sync mode from the database.
	syncMode, err := dsync.ReadSyncMode(dc, spr.source.Name)
	if err != nil {
		log.Error("unable to read sync mode: %v", err)
	}
	if syncMode != dsync.NoSync {
		spr.source.Status.Sync.Snapshot()
	}

	// dedup keeps track of "primary key not defined" and similar errors
	// that have been logged, in order to reduce duplication of the error
	// messages.
	dedup := log.NewMessageSet()

	spr.schemaPassFilter, err = util.CompileRegexps(spr.source.SchemaPassFilter)
	if err != nil {
		return err
	}
	spr.schemaStopFilter, err = util.CompileRegexps(spr.source.SchemaStopFilter)
	if err != nil {
		return err
	}
	spr.tableStopFilter, err = util.CompileRegexps(spr.source.TableStopFilter)
	if err != nil {
		return err
	}
	if spr.svr.opt.Script {
		var errString string
		processStream(0, nil, ctx, cat, spr, syncMode, dedup, nil, nil, 0, &errString)
		if errString != "" {
			spr.source.Status.Stream.Error()
			return errors.New(errString)
		}
		return nil
	}
	var brokers = spr.source.Brokers
	var topics = spr.source.Topics
	var group = spr.source.Group
	var maxPollInterval int
	if maxPollInterval, err = getConfigMaxPollInterval(cat); err != nil {
		return err
	}
	log.Debug("connecting to %q, topics %q", brokers, topics)
	log.Debug("connecting to source %q", spr.source.Name)
	var config = &kafka.ConfigMap{
		"auto.offset.reset":    "earliest",
		"bootstrap.servers":    brokers,
		"enable.auto.commit":   false,
		"group.id":             group,
		"max.poll.interval.ms": maxPollInterval,
		// The default range strategy assigns partition 0 to the same consumer for all topics.
		// We currently use round robin assignment which does not have this problem.
		// A custom partitioner could attempt to balance the consumers.
		"partition.assignment.strategy": "roundrobin",
		"security.protocol":             spr.source.Security,
	}

	var checkpointSegmentSize int
	if checkpointSegmentSize, err = getConfigCheckpointSegmentSize(cat); err != nil {
		return err
	}

	var consumersN int // Number of concurrent consumers
	if syncMode == dsync.NoSync {
		// During normal operation, we run single-threaded to give priority to user queries.
		consumersN = 1
	} else {
		// During a sync process, we can optionally enable concurrency.
		var kafkaConcurrency string
		kafkaConcurrency, err = cat.GetConfig("kafka_sync_concurrency")
		if err != nil {
			return err
		}
		consumersN, err = strconv.Atoi(kafkaConcurrency)
		if err != nil {
			return fmt.Errorf("invalid value %q for kafka_sync_concurrency", kafkaConcurrency)
		}
		if consumersN < 1 {
			consumersN = 1
		}
		if consumersN > 32 {
			consumersN = 32
		}
	}
	// First create the consumers.
	consumers := make([]*kafka.Consumer, consumersN)
	for i := 0; i < consumersN; i++ {
		consumers[i], err = kafka.NewConsumer(config)
		if err != nil {
			for j := 0; j < i; j++ {
				_ = consumers[j].Close()
			}
			spr.source.Status.Stream.Error()
			return err
		}
	}
	defer func(consumers []*kafka.Consumer) {
		for i := 0; i < consumersN; i++ {
			_ = consumers[i].Close()
		}
	}(consumers)
	// Next subscribe to the topics and register a rebalance callback which sets rebalanceFlag.
	var rebalanceFlag int32 // Atomic used to signal a rebalance
	for i := 0; i < consumersN; i++ {
		err = consumers[i].SubscribeTopics(topics, func(c *kafka.Consumer, event kafka.Event) error {
			atomic.StoreInt32(&rebalanceFlag, int32(1))
			return nil
		})
		if err != nil {
			spr.source.Status.Stream.Error()
			return err
		}
	}

	spr.source.Status.Stream.Active()

	// One thread (goroutine) per consumer runs a stream processor in a loop.
	// When a rebalance occurs, we synchronize the threads to prevent out-of-order database writes.
	var firstEvent int32 // Atomic used to log that data have been received
	atomic.StoreInt32(&firstEvent, int32(1))
	for {
		var waitStreamProcs sync.WaitGroup
		errStrings := make([]string, consumersN)
		atomic.StoreInt32(&rebalanceFlag, int32(0)) // Reset
		for i := 0; i < consumersN; i++ {
			waitStreamProcs.Add(1)
			go func(thread int, consumer *kafka.Consumer, ctx context.Context, cat *catalog.Catalog, spr *sproc, syncMode dsync.Mode, dedup *log.MessageSet, rebalanceFlag *int32, firstEvent *int32, errString *string) {
				defer waitStreamProcs.Done()
				processStream(thread, consumer, ctx, cat, spr, syncMode, dedup, rebalanceFlag, firstEvent, checkpointSegmentSize, errString)
			}(i, consumers[i], ctx, cat, spr, syncMode, dedup, &rebalanceFlag, &firstEvent, &(errStrings[i]))
		}

		waitStreamProcs.Wait() // Synchronize the threads

		// TODO This error handling is not quite right.
		for i := 0; i < consumersN; i++ {
			if errStrings[i] != "" {
				spr.source.Status.Stream.Error()
				return errors.New(errStrings[i])
			}
		}
		if spr.svr.opt.Script {
			break
		}
	}
	return nil
}

func getConfigMaxPollInterval(cat *catalog.Catalog) (int, error) {
	var m string
	var err error
	m, err = cat.GetConfig("max_poll_interval")
	if err != nil {
		return 0, err
	}
	var maxPollInterval int
	maxPollInterval, err = strconv.Atoi(m)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q for max_poll_interval", m)
	}
	return maxPollInterval, nil
}

func getConfigCheckpointSegmentSize(cat *catalog.Catalog) (int, error) {
	var c string
	var err error
	c, err = cat.GetConfig("checkpoint_segment_size")
	if err != nil {
		return 0, err
	}
	var checkpointSegmentSize int
	checkpointSegmentSize, err = strconv.Atoi(c)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q for checkpoint_segment_size", c)
	}
	return checkpointSegmentSize, nil
}

func processStream(thread int, consumer *kafka.Consumer, ctx context.Context, cat *catalog.Catalog, spr *sproc, syncMode dsync.Mode, dedup *log.MessageSet, rebalanceFlag *int32, firstEvent *int32, checkpointSegmentSize int, errString *string) {
	// Parameters spr and syncMode are not thread-safe and should not be modified during stream processing.

	for { // Stream processing main loop
		cmdgraph := command.NewCommandGraph()

		var eventReadCount int
		var err error
		// Parse
		if !spr.svr.opt.Script {
			eventReadCount, err = parseChangeEvents(cat, dedup, consumer, cmdgraph, spr.schemaPassFilter,
				spr.schemaStopFilter, spr.tableStopFilter, spr.source.TrimSchemaPrefix,
				spr.source.AddSchemaPrefix, spr.source.MapPublicSchema, spr.sourceLog,
				checkpointSegmentSize)
			if err != nil {
				*errString = fmt.Sprintf("parser: %v", err)
				return
			}
			if atomic.LoadInt32(firstEvent) == 1 {
				atomic.StoreInt32(firstEvent, int32(0))
				log.Debug("receiving data from source %q", spr.source.Name)
			}
		} else {
			cmdgraph = spr.svr.opt.ScriptOpts.CmdGraph
			eventReadCount = cmdgraph.Commands.Len()
		}

		// Rewrite
		if err = rewriteCommandGraph(cat, cmdgraph); err != nil {
			*errString = fmt.Sprintf("rewriter: %v", err)
			return
		}

		// Execute
		if err = execCommandGraph(thread, ctx, cat, cmdgraph, spr.svr.dp, spr.source.Name, spr.svr.opt.UUOpt, syncMode, dedup); err != nil {
			*errString = fmt.Sprintf("executor: %v", err)
			return
		}

		if !spr.svr.opt.Script {
			// Commit Kafka consumer
			if eventReadCount > 0 && !spr.svr.opt.NoKafkaCommit {
				_, err = consumer.Commit()
				if err != nil {
					e := err.(kafka.Error)
					if e.IsFatal() {
						//return fmt.Errorf("Kafka commit: %v", e)
						log.Warning("Kafka commit: %v", e)
					} else {
						switch e.Code() {
						case kafka.ErrNoOffset:
							log.Debug("Kafka commit: %v", e)
						default:
							log.Info("Kafka commit: %v", e)
						}
					}
				}
			}
		}

		if eventReadCount > 0 {
			log.Debug("[%d] checkpoint: events=%d, commands=%d", thread, eventReadCount, cmdgraph.Commands.Len())
		}

		// Check if sync snapshot may have completed.
		if syncMode != dsync.NoSync {
			if spr.source.Status.Stream.Get() == status.StreamActive && cat.HoursSinceLastSnapshotRecord() > 3.0 {
				spr.source.Status.Sync.SnapshotComplete()
				msg := fmt.Sprintf("source %q snapshot complete (deadline exceeded); consider running \"metadb endsync\"",
					spr.source.Name)
				if dedup.Insert(msg) {
					log.Info("%s", msg)
				}
			} else {
				spr.source.Status.Sync.Snapshot()
			}
		}

		if !spr.svr.opt.Script {
			if atomic.LoadInt32(rebalanceFlag) == 1 { // Exit thread on rebalance
				log.Trace("[%d] rebalance", thread)
				break
			}
		}

		if spr.svr.opt.Script {
			break
		}
	}

}

func parseChangeEvents(cat *catalog.Catalog, dedup *log.MessageSet, consumer *kafka.Consumer, cmdgraph *command.CommandGraph, schemaPassFilter, schemaStopFilter, tableStopFilter []*regexp.Regexp, trimSchemaPrefix, addSchemaPrefix, mapPublicSchema string, sourceLog *log.SourceLog, checkpointSegmentSize int) (int, error) {
	kafkaPollTimeout := 100     // Poll timeout in milliseconds.
	pollTimeoutCountLimit := 20 // Maximum allowable number of consecutive poll timeouts.
	pollLoopTimeout := 120.0    // Overall pool loop timeout in seconds.
	snapshot := false
	var eventReadCount int
	pollTimeoutCount := 0
	startTime := time.Now()
	for x := 0; x < checkpointSegmentSize; x++ {
		// Catch the possibility of many poll timeouts between messages, because each
		// poll timeouts takes kafkaPollTimeout ms.  This also provides an overall timeout
		// for the poll loop.
		if time.Since(startTime).Seconds() >= pollLoopTimeout {
			log.Trace("poll timeout")
			break
		}
		var err error
		var msg *kafka.Message
		if msg, err = readChangeEvent(consumer, sourceLog, kafkaPollTimeout); err != nil {
			return 0, fmt.Errorf("reading message from Kafka: %w", err)
		}
		if msg == nil { // Poll timeout is indicated by the nil return.
			pollTimeoutCount++
			if pollTimeoutCount >= pollTimeoutCountLimit {
				break // Prevent processing of a small batch from being delayed.
			} else {
				continue
			}
		} else {
			pollTimeoutCount = 0 // We are only interested in consecutive timeouts.
		}
		eventReadCount++

		var ce *change.Event
		ce, err = change.NewEvent(msg)
		if err != nil {
			log.Error("%s", err)
			ce = nil
		}

		c, snap, err := command.NewCommand(cat, dedup, ce, schemaPassFilter, schemaStopFilter, tableStopFilter,
			trimSchemaPrefix, addSchemaPrefix, mapPublicSchema)
		if err != nil {
			log.Debug("%v", *ce)
			return 0, fmt.Errorf("parsing command: %w", err)
		}
		if c == nil {
			continue
		}
		if snap {
			snapshot = true
		}
		_ = cmdgraph.Commands.PushBack(c)
	}
	commandsN := cmdgraph.Commands.Len()
	if commandsN > 0 {
		log.Trace("read %d events", commandsN)
	}
	if snapshot {
		cat.ResetLastSnapshotRecord()
	}
	return eventReadCount, nil
}

func readChangeEvent(consumer *kafka.Consumer, sourceLog *log.SourceLog, kafkaPollTimeout int) (*kafka.Message, error) {
	ev := consumer.Poll(kafkaPollTimeout)
	if ev == nil {
		return nil, nil
	}
	switch e := ev.(type) {
	case *kafka.Message:
		msg := e
		if msg != nil { // received message
			if sourceLog != nil {
				sourceLog.Log("#")
				sourceLog.Log(string(msg.Key))
				sourceLog.Log(string(msg.Value))
			}
		}
		return e, nil
	//case kafka.PartitionEOF:
	//	log.Trace("%s", e)
	//	return nil, nil
	case kafka.Error:
		// In general, errors from the Kafka
		// client can be reported and ignored,
		// because the client will
		// automatically try to recover.
		if e.IsFatal() {
			log.Warning("Kafka poll: %v", e)
		} else {
			log.Info("Kafka poll: %v", e)
		}
		// We could take some action if
		// desired:
		//if e.Code() == kafka.ErrAllBrokersDown {
		//        // some action
		//}
	default:
		log.Debug("ignoring: %v", e)
	}
	return nil, nil
}

func logTraceCommand(thread int, c *command.Command) {
	var schemaTable string
	if c.SchemaName == "" {
		schemaTable = c.TableName
	} else {
		schemaTable = c.SchemaName + "." + c.TableName
	}
	var pkey = command.PrimaryKeyColumns(c.Column)
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, "[%d] %s: %s", thread, c.Op, schemaTable)
	if c.Op != command.TruncateOp {
		_, _ = fmt.Fprintf(&b, " (")
		var x int
		var col command.CommandColumn
		for x, col = range pkey {
			if x > 0 {
				_, _ = fmt.Fprintf(&b, ", ")
			}
			_, _ = fmt.Fprintf(&b, "%s=%v", col.Name, col.Data)
		}
		_, _ = fmt.Fprintf(&b, ")")
	}
	log.Trace("%s", b.String())
}

func waitForConfig(svr *server) (*sproc, error) {
	var databases = dbxToConnector(svr.db)
	var sources []*sysdb.SourceConnector
	var ready bool
	var err error
	for {
		sources, ready, err = waitForConfigSource(svr)
		if err != nil {
			return nil, err
		}
		if ready {
			break
		}
	}
	var src *sysdb.SourceConnector = sources[0]
	var spr = &sproc{
		source:    src,
		databases: databases,
		svr:       svr,
	}
	return spr, nil
}

func waitForConfigSource(svr *server) ([]*sysdb.SourceConnector, bool, error) {
	svr.state.mu.Lock()
	defer svr.state.mu.Unlock()

	// var databases []*sysdb.DatabaseConnector
	var sources []*sysdb.SourceConnector
	var err error
	// if databases, err = sysdb.ReadDatabaseConnectors(); err != nil {
	// 	return nil, nil, false, err
	// }
	if sources, err = sysdb.ReadSourceConnectors(svr.db); err != nil {
		return nil, false, err
	}
	if len(sources) > 0 {
		if sources[0].Enable {
			// Reread connectors in case configuration was incomplete.
			if sources, err = sysdb.ReadSourceConnectors(svr.db); err != nil {
				return nil, false, err
			}
			if len(sources) > 0 {
				sources[0].Status.Stream.Waiting()
				svr.state.sources = sources
			}
			return sources, true, nil
		}
	}
	time.Sleep(2 * time.Second)
	return nil, false, nil
}

func dbxToConnector(db *dbx.DB) []*sysdb.DatabaseConnector {
	var dbcs = make([]*sysdb.DatabaseConnector, 0)
	dbcs = append(dbcs, &sysdb.DatabaseConnector{
		DBHost:          db.Host,
		DBPort:          db.Port,
		DBName:          db.DBName,
		DBAdminUser:     db.User,
		DBAdminPassword: db.Password,
		DBSuperUser:     db.SuperUser,
		DBSuperPassword: db.SuperPassword,
	})
	return dbcs
}

package main

import (
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/felixge/fgprof"
	"github.com/gorilla/mux"
	"github.com/jpillora/overseer"
	"github.com/sirupsen/logrus"
	"gopkg.in/alecthomas/kingpin.v2"

	"github.com/trufflesecurity/trufflehog/v3/pkg/common"
	"github.com/trufflesecurity/trufflehog/v3/pkg/config"
	"github.com/trufflesecurity/trufflehog/v3/pkg/context"
	"github.com/trufflesecurity/trufflehog/v3/pkg/decoders"
	"github.com/trufflesecurity/trufflehog/v3/pkg/engine"
	"github.com/trufflesecurity/trufflehog/v3/pkg/log"
	"github.com/trufflesecurity/trufflehog/v3/pkg/output"
	"github.com/trufflesecurity/trufflehog/v3/pkg/sources"
	"github.com/trufflesecurity/trufflehog/v3/pkg/sources/git"
	"github.com/trufflesecurity/trufflehog/v3/pkg/updater"
	"github.com/trufflesecurity/trufflehog/v3/pkg/version"
)

var (
	cli              = kingpin.New("TruffleHog", "TruffleHog is a tool for finding credentials.")
	cmd              string
	debug            = cli.Flag("debug", "Run in debug mode.").Bool()
	trace            = cli.Flag("trace", "Run in trace mode.").Bool()
	jsonOut          = cli.Flag("json", "Output in JSON format.").Short('j').Bool()
	jsonLegacy       = cli.Flag("json-legacy", "Use the pre-v3.0 JSON format. Only works with git, gitlab, and github sources.").Bool()
	concurrency      = cli.Flag("concurrency", "Number of concurrent workers.").Default(strconv.Itoa(runtime.NumCPU())).Int()
	noVerification   = cli.Flag("no-verification", "Don't verify the results.").Bool()
	onlyVerified     = cli.Flag("only-verified", "Only output verified results.").Bool()
	filterUnverified = cli.Flag("filter-unverified", "Only output first unverified result per chunk per detector if there are more than one results.").Bool()
	configFilename   = cli.Flag("config", "Path to configuration file.").ExistingFile()
	// rules = cli.Flag("rules", "Path to file with custom rules.").String()
	printAvgDetectorTime = cli.Flag("print-avg-detector-time", "Print the average time spent on each detector.").Bool()
	noUpdate             = cli.Flag("no-update", "Don't check for updates.").Bool()
	fail                 = cli.Flag("fail", "Exit with code 183 if results are found.").Bool()

	gitScan             = cli.Command("git", "Find credentials in git repositories.")
	gitScanURI          = gitScan.Arg("uri", "Git repository URL. https://, file://, or ssh:// schema expected.").Required().String()
	gitScanIncludePaths = gitScan.Flag("include-paths", "Path to file with newline separated regexes for files to include in scan.").Short('i').String()
	gitScanExcludePaths = gitScan.Flag("exclude-paths", "Path to file with newline separated regexes for files to exclude in scan.").Short('x').String()
	gitScanSinceCommit  = gitScan.Flag("since-commit", "Commit to start scan from.").String()
	gitScanBranch       = gitScan.Flag("branch", "Branch to scan.").String()
	gitScanMaxDepth     = gitScan.Flag("max-depth", "Maximum depth of commits to scan.").Int()
	_                   = gitScan.Flag("allow", "No-op flag for backwards compat.").Bool()
	_                   = gitScan.Flag("entropy", "No-op flag for backwards compat.").Bool()
	_                   = gitScan.Flag("regex", "No-op flag for backwards compat.").Bool()

	githubScan           = cli.Command("github", "Find credentials in GitHub repositories.")
	githubScanEndpoint   = githubScan.Flag("endpoint", "GitHub endpoint.").Default("https://api.github.com").String()
	githubScanRepos      = githubScan.Flag("repo", `GitHub repository to scan. You can repeat this flag. Example: "https://github.com/dustin-decker/secretsandstuff"`).Strings()
	githubScanOrgs       = githubScan.Flag("org", `GitHub organization to scan. You can repeat this flag. Example: "trufflesecurity"`).Strings()
	githubScanToken      = githubScan.Flag("token", "GitHub token. Can be provided with environment variable GITHUB_TOKEN.").Envar("GITHUB_TOKEN").String()
	githubIncludeForks   = githubScan.Flag("include-forks", "Include forks in scan.").Bool()
	githubIncludeMembers = githubScan.Flag("include-members", "Include organization member repositories in scan.").Bool()
	githubIncludeRepos   = githubScan.Flag("include-repos", `Repositories to include in an org scan. This can also be a glob pattern. You can repeat this flag. Must use Github repo full name. Example: "trufflesecurity/trufflehog", "trufflesecurity/t*"`).Strings()
	githubExcludeRepos   = githubScan.Flag("exclude-repos", `Repositories to exclude in an org scan. This can also be a glob pattern. You can repeat this flag. Must use Github repo full name. Example: "trufflesecurity/driftwood", "trufflesecurity/d*"`).Strings()

	gitlabScan = cli.Command("gitlab", "Find credentials in GitLab repositories.")
	// TODO: Add more GitLab options
	gitlabScanEndpoint     = gitlabScan.Flag("endpoint", "GitLab endpoint.").Default("https://gitlab.com").String()
	gitlabScanRepos        = gitlabScan.Flag("repo", "GitLab repo url. You can repeat this flag. Leave empty to scan all repos accessible with provided credential. Example: https://gitlab.com/org/repo.git").Strings()
	gitlabScanToken        = gitlabScan.Flag("token", "GitLab token. Can be provided with environment variable GITLAB_TOKEN.").Envar("GITLAB_TOKEN").Required().String()
	gitlabScanIncludePaths = gitlabScan.Flag("include-paths", "Path to file with newline separated regexes for files to include in scan.").Short('i').String()
	gitlabScanExcludePaths = gitlabScan.Flag("exclude-paths", "Path to file with newline separated regexes for files to exclude in scan.").Short('x').String()

	filesystemScan        = cli.Command("filesystem", "Find credentials in a filesystem.")
	filesystemDirectories = filesystemScan.Flag("directory", "Path to directory to scan. You can repeat this flag.").Required().Strings()
	// TODO: Add more filesystem scan options. Currently only supports scanning a list of directories.
	// filesystemScanRecursive = filesystemScan.Flag("recursive", "Scan recursively.").Short('r').Bool()
	// filesystemScanIncludePaths = filesystemScan.Flag("include-paths", "Path to file with newline separated regexes for files to include in scan.").Short('i').String()
	// filesystemScanExcludePaths = filesystemScan.Flag("exclude-paths", "Path to file with newline separated regexes for files to exclude in scan.").Short('x').String()

	s3Scan         = cli.Command("s3", "Find credentials in S3 buckets.")
	s3ScanKey      = s3Scan.Flag("key", "S3 key used to authenticate. Can be provided with environment variable AWS_ACCESS_KEY_ID.").Envar("AWS_ACCESS_KEY_ID").String()
	s3ScanSecret   = s3Scan.Flag("secret", "S3 secret used to authenticate. Can be provided with environment variable AWS_SECRET_ACCESS_KEY.").Envar("AWS_SECRET_ACCESS_KEY").String()
	s3ScanCloudEnv = s3Scan.Flag("cloud-environment", "Use IAM credentials in cloud environment.").Bool()
	s3ScanBuckets  = s3Scan.Flag("bucket", "Name of S3 bucket to scan. You can repeat this flag.").Strings()

	syslogScan     = cli.Command("syslog", "Scan syslog")
	syslogAddress  = syslogScan.Flag("address", "Address and port to listen on for syslog. Example: 127.0.0.1:514").String()
	syslogProtocol = syslogScan.Flag("protocol", "Protocol to listen on. udp or tcp").String()
	syslogTLSCert  = syslogScan.Flag("cert", "Path to TLS cert.").String()
	syslogTLSKey   = syslogScan.Flag("key", "Path to TLS key.").String()
	syslogFormat   = syslogScan.Flag("format", "Log format. Can be rfc3164 or rfc5424").String()

	circleCiScan      = cli.Command("circleci", "Scan CircleCI")
	circleCiScanToken = circleCiScan.Flag("token", "CircleCI token. Can also be provided with environment variable").Envar("CIRCLECI_TOKEN").Required().String()
)

func init() {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--") {
			split := strings.SplitN(arg, "=", 2)
			split[0] = strings.ReplaceAll(split[0], "_", "-")
			os.Args[i] = strings.Join(split, "=")
		}
	}

	cli.Version("trufflehog " + version.BuildVersion)
	cmd = kingpin.MustParse(cli.Parse(os.Args[1:]))

	if *jsonOut {
		logrus.SetFormatter(&logrus.JSONFormatter{})
	}
	switch {
	case *trace:
		log.SetLevel(5)
		logrus.SetLevel(logrus.TraceLevel)
		logrus.Debugf("running version %s", version.BuildVersion)
	case *debug:
		log.SetLevel(2)
		logrus.SetLevel(logrus.DebugLevel)
		logrus.Debugf("running version %s", version.BuildVersion)
	default:
		logrus.SetLevel(logrus.InfoLevel)
	}
}

func main() {
	updateCfg := overseer.Config{
		Program:       run,
		Debug:         *debug,
		RestartSignal: syscall.SIGTERM,
		// TODO: Eventually add a PreUpgrade func for signature check w/ x509 PKCS1v15
		// PreUpgrade: checkUpdateSignature(binaryPath string),
	}

	if !*noUpdate {
		updateCfg.Fetcher = updater.Fetcher(version.BuildVersion)
	}
	if version.BuildVersion == "dev" {
		updateCfg.Fetcher = nil
	}

	err := overseer.RunErr(updateCfg)
	if err != nil {
		logrus.WithError(err).Fatal("error occured with trufflehog updater 🐷")
	}
}

func run(state overseer.State) {
	if *debug {
		logrus.Debugf("trufflehog %s", version.BuildVersion)
	}

	if *githubScanToken != "" {
		// NOTE: this kludge is here to do an authenticated shallow commit
		// TODO: refactor to better pass credentials
		os.Setenv("GITHUB_TOKEN", *githubScanToken)
	}

	// When setting a base commit, chunks must be scanned in order.
	if *gitScanSinceCommit != "" {
		*concurrency = 1
	}

	if *debug {
		go func() {
			router := mux.NewRouter()
			router.PathPrefix("/debug/pprof").Handler(http.DefaultServeMux)
			router.PathPrefix("/debug/fgprof").Handler(fgprof.Handler())
			logrus.Info("starting pprof and fgprof server on :18066 /debug/pprof and /debug/fgprof")
			if err := http.ListenAndServe(":18066", router); err != nil {
				logrus.Error(err)
			}
		}()
	}
	logger, sync := log.New("trufflehog", log.WithConsoleSink(os.Stderr))
	context.SetDefaultLogger(logger)
	defer func() { _ = sync() }()

	conf := &config.Config{}
	if *configFilename != "" {
		var err error
		conf, err = config.Read(*configFilename)
		if err != nil {
			logger.Error(err, "error parsing the provided configuration file")
			os.Exit(1)
		}
	}

	ctx := context.TODO()
	e := engine.Start(ctx,
		engine.WithConcurrency(*concurrency),
		engine.WithDecoders(decoders.DefaultDecoders()...),
		engine.WithDetectors(!*noVerification, engine.DefaultDetectors()...),
		engine.WithDetectors(!*noVerification, conf.Detectors...),
		engine.WithFilterUnverified(*filterUnverified),
	)

	filter, err := common.FilterFromFiles(*gitScanIncludePaths, *gitScanExcludePaths)
	if err != nil {
		logrus.WithError(err).Fatal("could not create filter")
	}

	var repoPath string
	var remote bool
	switch cmd {
	case gitScan.FullCommand():
		repoPath, remote, err = git.PrepareRepoSinceCommit(ctx, *gitScanURI, *gitScanSinceCommit)
		if err != nil || repoPath == "" {
			logrus.WithError(err).Fatal("error preparing git repo for scanning")
		}
		if remote {
			defer os.RemoveAll(repoPath)
		}

		g := func(c *sources.Config) {
			c.RepoPath = repoPath
			c.HeadRef = *gitScanBranch
			c.BaseRef = *gitScanSinceCommit
			c.MaxDepth = *gitScanMaxDepth
			c.Filter = filter
		}

		if err = e.ScanGit(ctx, sources.NewConfig(g)); err != nil {
			logrus.WithError(err).Fatal("Failed to scan Git.")
		}
	case githubScan.FullCommand():
		if len(*githubScanOrgs) == 0 && len(*githubScanRepos) == 0 {
			logrus.Fatal("You must specify at least one organization or repository.")
		}

		github := func(c *sources.Config) {
			c.Endpoint = *githubScanEndpoint
			c.Repos = *githubScanRepos
			c.Orgs = *githubScanOrgs
			c.Token = *githubScanToken
			c.IncludeForks = *githubIncludeForks
			c.IncludeMembers = *githubIncludeMembers
			c.Concurrency = *concurrency
			c.ExcludeRepos = *githubExcludeRepos
			c.IncludeRepos = *githubIncludeRepos
		}

		if err = e.ScanGitHub(ctx, sources.NewConfig(github)); err != nil {
			logrus.WithError(err).Fatal("Failed to scan Github.")
		}
	case gitlabScan.FullCommand():
		filter, err := common.FilterFromFiles(*gitlabScanIncludePaths, *gitlabScanExcludePaths)
		if err != nil {
			logrus.WithError(err).Fatal("could not create filter")
		}

		gitlab := func(c *sources.Config) {
			c.Endpoint = *gitlabScanEndpoint
			c.Token = *gitlabScanToken
			c.Repos = *gitlabScanRepos
			c.Filter = filter
		}

		if err = e.ScanGitLab(ctx, sources.NewConfig(gitlab)); err != nil {
			logrus.WithError(err).Fatal("Failed to scan GitLab.")
		}
	case filesystemScan.FullCommand():
		fs := func(c *sources.Config) {
			c.Directories = *filesystemDirectories
		}

		if err = e.ScanFileSystem(ctx, sources.NewConfig(fs)); err != nil {
			logrus.WithError(err).Fatal("Failed to scan filesystem")
		}
	case s3Scan.FullCommand():
		s3 := func(c *sources.Config) {
			c.Key = *s3ScanKey
			c.Secret = *s3ScanSecret
			c.Buckets = *s3ScanBuckets
		}

		if err = e.ScanS3(ctx, sources.NewConfig(s3)); err != nil {
			logrus.WithError(err).Fatal("Failed to scan S3.")
		}
	case syslogScan.FullCommand():
		syslog := func(c *sources.Config) {
			c.Address = *syslogAddress
			c.Protocol = *syslogProtocol
			c.CertPath = *syslogTLSCert
			c.KeyPath = *syslogTLSKey
			c.Format = *syslogFormat
			c.Concurrency = *concurrency
		}

		if err = e.ScanSyslog(ctx, sources.NewConfig(syslog)); err != nil {
			logrus.WithError(err).Fatal("Failed to scan syslog.")
		}
	case circleCiScan.FullCommand():
		if err = e.ScanCircleCI(ctx, *circleCiScanToken); err != nil {
			logrus.WithError(err).Fatal("Failed to scan CircleCI.")
		}
	}
	// asynchronously wait for scanning to finish and cleanup
	go e.Finish(ctx)

	if !*jsonLegacy && !*jsonOut {
		fmt.Fprintf(os.Stderr, "🐷🔑🐷  TruffleHog. Unearth your secrets. 🐷🔑🐷\n\n")
	}

	// NOTE: this loop will terminate when the results channel is closed in
	// e.Finish()
	foundResults := false
	for r := range e.ResultsChan() {
		if *onlyVerified && !r.Verified {
			continue
		}
		foundResults = true

		switch {
		case *jsonLegacy:
			output.PrintLegacyJSON(ctx, &r)
		case *jsonOut:
			output.PrintJSON(&r)
		default:
			output.PrintPlainOutput(&r)
		}
	}
	logrus.Debugf("scanned %d chunks", e.ChunksScanned())
	logrus.Debugf("scanned %d bytes", e.BytesScanned())

	if *printAvgDetectorTime {
		printAverageDetectorTime(e)
	}

	if foundResults && *fail {
		logrus.Debug("exiting with code 183 because results were found")
		os.Exit(183)
	}
}

func printAverageDetectorTime(e *engine.Engine) {
	fmt.Fprintln(os.Stderr, "Average detector time is the measurement of average time spent on each detector when results are returned.")
	for detectorName, durations := range e.DetectorAvgTime() {
		var total time.Duration
		for _, d := range durations {
			total += d
		}
		avgDuration := total / time.Duration(len(durations))
		fmt.Fprintf(os.Stderr, "%s: %s\n", detectorName, avgDuration)
	}
}

package main

import (
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Sirupsen/logrus"
	"github.com/cardigann/cardigann/config"
	"github.com/cardigann/cardigann/indexer"
	"github.com/cardigann/cardigann/logger"
	"github.com/cardigann/cardigann/server"
	"github.com/cardigann/cardigann/torznab"
	"github.com/kardianos/service"
	"gopkg.in/alecthomas/kingpin.v2"
)

var (
	Version string
	log     = logger.Logger
)

func main() {
	os.Exit(run(os.Args[1:]...))
}

func run(args ...string) (exitCode int) {
	app := kingpin.New("cardigann",
		`A torznab proxy for torrent indexer sites`)

	app.Version(Version)
	app.Writer(os.Stdout)
	app.DefaultEnvars()

	app.Terminate(func(code int) {
		exitCode = code
	})

	app.Flag("debug", "Print out debug logging").Action(func(c *kingpin.ParseContext) error {
		logger.SetLevel(logrus.DebugLevel)
		return nil
	}).Bool()

	if err := configureServerCommand(app); err != nil {
		log.Error(err)
		return 1
	}

	configureQueryCommand(app)
	configureDownloadCommand(app)
	configureTestDefinitionCommand(app)
	configureServiceCommand(app)

	kingpin.MustParse(app.Parse(args))
	return
}

func lookupIndexer(key string) (*indexer.Runner, error) {
	conf, err := config.NewJSONConfig()
	if err != nil {
		return nil, err
	}

	def, err := indexer.LoadDefinition(key)
	if err != nil {
		return nil, err
	}

	return indexer.NewRunner(def, conf), nil
}

func configureQueryCommand(app *kingpin.Application) {
	var key, format string
	var args []string

	cmd := app.Command("query", "Manually query an indexer using torznab commands")
	cmd.Alias("q")
	cmd.Flag("format", "Either json, xml or rss").
		Default("json").
		Short('f').
		EnumVar(&format, "xml", "json", "rss")

	cmd.Arg("key", "The indexer key").
		Required().
		StringVar(&key)

	cmd.Arg("args", "Arguments to use to query").
		StringsVar(&args)

	cmd.Action(func(c *kingpin.ParseContext) error {
		return queryCommand(key, format, args)
	})
}

func queryCommand(key, format string, args []string) error {
	indexer, err := lookupIndexer(key)
	if err != nil {
		return err
	}

	vals := url.Values{}
	for _, arg := range args {
		tokens := strings.SplitN(arg, "=", 2)
		if len(tokens) == 1 {
			vals.Set("q", tokens[0])
		} else {
			vals.Add(tokens[0], tokens[1])
		}
	}

	query, err := torznab.ParseQuery(vals)
	if err != nil {
		return fmt.Errorf("Parsing query failed: %s", err.Error())
	}

	feed, err := indexer.Search(query)
	if err != nil {
		return fmt.Errorf("Searching failed: %s", err.Error())
	}

	switch format {
	case "xml":
		x, err := xml.MarshalIndent(feed, "", "  ")
		if err != nil {
			return fmt.Errorf("Failed to marshal XML: %s", err.Error())
		}
		fmt.Printf("%s", x)

	case "json":
		j, err := json.MarshalIndent(feed, "", "  ")
		if err != nil {
			return fmt.Errorf("Failed to marshal JSON: %s", err.Error())
		}
		fmt.Printf("%s", j)
	}

	return nil
}

func configureDownloadCommand(app *kingpin.Application) {
	var key, url, file string

	cmd := app.Command("download", "Download a torrent from the tracker")
	cmd.Arg("key", "The indexer key").
		Required().
		StringVar(&key)

	cmd.Arg("url", "The url of the file to download").
		Required().
		StringVar(&url)

	cmd.Arg("file", "The filename to download to").
		Required().
		StringVar(&file)

	cmd.Action(func(c *kingpin.ParseContext) error {
		return downloadCommand(key, url, file)
	})
}

func downloadCommand(key, url, file string) error {
	indexer, err := lookupIndexer(key)
	if err != nil {
		return err
	}

	rc, _, err := indexer.Download(url)
	if err != nil {
		return fmt.Errorf("Downloading failed: %s", err.Error())
	}

	defer rc.Close()

	f, err := os.Create(file)
	if err != nil {
		return fmt.Errorf("Creating file failed: %s", err.Error())
	}

	n, err := io.Copy(f, rc)
	if err != nil {
		return fmt.Errorf("Creating file failed: %s", err.Error())
	}

	log.WithFields(logrus.Fields{"bytes": n}).Info("Downloading file")
	return nil
}

func configureServerCommand(app *kingpin.Application) error {
	var bindPort, bindAddr, password string

	conf, err := config.NewJSONConfig()
	if err != nil {
		return err
	}

	defaultBind, err := config.GetGlobalConfig("bind", "0.0.0.0", conf)
	if err != nil {
		return err
	}

	defaultPort, err := config.GetGlobalConfig("port", "5060", conf)
	if err != nil {
		return err
	}

	cmd := app.Command("server", "Run the proxy (and web) server")
	cmd.Flag("port", "The port to listen on").
		OverrideDefaultFromEnvar("PORT").
		Default(defaultPort).
		StringVar(&bindPort)

	cmd.Flag("bind", "The address to bind to").
		Default(defaultBind).
		StringVar(&bindAddr)

	cmd.Flag("passphrase", "Require a passphrase to view web interface").
		Short('p').
		StringVar(&password)

	cmd.Action(func(c *kingpin.ParseContext) error {
		return serverCommand(bindAddr, bindPort, password)
	})

	return nil
}

func serverCommand(addr, port string, password string) error {
	conf, err := config.NewJSONConfig()
	if err != nil {
		return err
	}

	listenOn := fmt.Sprintf("%s:%s", addr, port)
	log.Infof("Listening on %s", listenOn)

	h, err := server.NewHandler(server.Params{
		Passphrase: password,
		Config:     conf,
	})
	if err != nil {
		return err
	}

	return http.ListenAndServe(listenOn, h)
}

func configureTestDefinitionCommand(app *kingpin.Application) {
	var f *os.File

	cmd := app.Command("test-definition", "Test a yaml indexer definition file")
	cmd.Alias("test")

	cmd.Arg("file", "The definition yaml file").
		Required().
		FileVar(&f)

	cmd.Action(func(c *kingpin.ParseContext) error {
		return testDefinitionCommand(f)
	})
}

func testDefinitionCommand(f *os.File) error {
	conf, err := config.NewJSONConfig()
	if err != nil {
		return err
	}

	def, err := indexer.ParseDefinitionFile(f)
	if err != nil {
		return err
	}

	fmt.Println("Definition file parsing OK")

	runner := indexer.NewRunner(def, conf)
	tester := indexer.Tester{Runner: runner, Opts: indexer.TesterOpts{
		Download: true,
	}}

	err = tester.Test()
	if err != nil {
		return fmt.Errorf("Test failed: %s", err.Error())
	}

	fmt.Println("Indexer test returned OK")
	return nil
}

func configureServiceCommand(app *kingpin.Application) {
	var action string
	var userService bool
	var possibleActions = append(service.ControlAction[:], "run")

	cmd := app.Command("service", "Control the cardigann service")

	cmd.Flag("user", "Whether to use a user service rather than a system one").
		BoolVar(&userService)

	cmd.Arg("action", "One of "+strings.Join(possibleActions, ", ")).
		Required().
		EnumVar(&action, possibleActions...)

	cmd.Action(func(c *kingpin.ParseContext) error {
		log.Debugf("Running service action %s on platform %v.", action, service.Platform())

		prg, err := newProgram(programOpts{
			UserService: userService,
		})
		if err != nil {
			return err
		}

		if action != "run" {
			return service.Control(prg.service, action)
		}

		return runServiceCommand(prg)
	})
}

func runServiceCommand(prg *program) error {
	var err error
	errs := make(chan error)
	prg.logger, err = prg.service.Logger(errs)
	if err != nil {
		log.Fatal(err)
	}

	logger.SetOutput(ioutil.Discard)
	logger.AddHook(&serviceLogHook{prg.logger})
	logger.SetFormatter(&serviceLogFormatter{})

	go func() {
		for {
			err := <-errs
			if err != nil {
				log.Error(err)
			}
		}
	}()

	err = prg.service.Run()
	if err != nil {
		prg.logger.Error(err)
	}

	return nil
}

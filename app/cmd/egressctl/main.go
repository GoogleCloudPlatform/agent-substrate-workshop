// Command egressctl applies, shows, and deletes actor EgressPolicies through the ateapi
// gRPC API. Run it through `make egress` (which handles port-forward, token, and CA),
// or directly after `source scripts/ate-api.sh`.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/agent-substrate/workshop/app/internal/ateclient"
	"github.com/agent-substrate/workshop/app/internal/egress"
	"google.golang.org/protobuf/encoding/protojson"
	"sigs.k8s.io/yaml"
)

const usage = `usage: egressctl [flags] apply -f FILE (--actor NAME | --all)
       egressctl [flags] get --actor NAME
       egressctl [flags] delete (--actor NAME | --all)`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "egressctl:", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func run(ctx context.Context, args []string, out io.Writer) error {
	global := flag.NewFlagSet("egressctl", flag.ContinueOnError)
	global.SetOutput(io.Discard)
	atespace := global.String("a", envOr("ATESPACE", "workshop"), "atespace")
	var o ateclient.Options
	global.StringVar(&o.Addr, "addr", envOr("ATE_API_ADDR", "localhost:8443"), "ateapi host:port")
	global.StringVar(&o.TokenFile, "token-file", os.Getenv("ATE_TOKEN_FILE"), "bearer token file")
	global.StringVar(&o.CAFile, "ca-file", os.Getenv("ATE_CA_FILE"), "ateapi CA bundle (PEM)")
	global.StringVar(&o.ServerName, "server-name", ateclient.DefaultServerName, "TLS server name")
	global.BoolVar(&o.Insecure, "insecure", os.Getenv("ATE_API_INSECURE") == "true", "skip TLS verification")
	if err := global.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}
	rest := global.Args()
	if len(rest) == 0 {
		return errors.New(usage)
	}
	cmd := flag.NewFlagSet(rest[0], flag.ContinueOnError)
	cmd.SetOutput(io.Discard)
	file := cmd.String("f", "", "policy file (apply)")
	actor := cmd.String("actor", "", "actor name")
	all := cmd.Bool("all", false, "every actor in the atespace")
	if err := cmd.Parse(rest[1:]); err != nil {
		return fmt.Errorf("%w\n%s", err, usage)
	}

	c, closer, err := ateclient.Dial(o)
	if err != nil {
		return err
	}
	defer closer.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	switch rest[0] {
	case "apply":
		if *file == "" {
			return errors.New("apply needs -f FILE\n" + usage)
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		p, err := egress.ParsePolicy(data)
		if err != nil {
			return err
		}
		refs, err := egress.Targets(ctx, c, *atespace, *actor, *all)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			created, err := egress.Apply(ctx, c, ref, p)
			if err != nil {
				return err
			}
			verb := "updated"
			if created {
				verb = "created"
			}
			fmt.Fprintf(out, "%s %s/%s\n", verb, ref.GetAtespace(), ref.GetName())
		}
	case "get":
		if *actor == "" {
			return errors.New("get needs --actor NAME\n" + usage)
		}
		refs, _ := egress.Targets(ctx, c, *atespace, *actor, false)
		p, err := egress.Get(ctx, c, refs[0])
		if err != nil {
			return err
		}
		j, err := protojson.Marshal(p)
		if err != nil {
			return err
		}
		y, err := yaml.JSONToYAML(j)
		if err != nil {
			return err
		}
		_, _ = out.Write(y)
	case "delete":
		refs, err := egress.Targets(ctx, c, *atespace, *actor, *all)
		if err != nil {
			return err
		}
		for _, ref := range refs {
			if err := egress.Delete(ctx, c, ref); err != nil {
				return err
			}
			fmt.Fprintf(out, "deleted %s/%s\n", ref.GetAtespace(), ref.GetName())
		}
	default:
		return fmt.Errorf("unknown command %q\n%s", rest[0], usage)
	}
	return nil
}

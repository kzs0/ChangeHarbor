// reviewd exposes offline, shadow-only vertical slices. It never submits approval.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/kzs0/reviewd/internal/contextpack"
	gitreader "github.com/kzs0/reviewd/internal/git"
	"github.com/kzs0/reviewd/internal/model"
	riskpolicy "github.com/kzs0/reviewd/internal/policy"
	"github.com/kzs0/reviewd/internal/release"
	"github.com/kzs0/reviewd/internal/report"
	"github.com/kzs0/reviewd/internal/review"
	"github.com/kzs0/reviewd/internal/telemetry"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cleanup := telemetry.Init(signalCtx, os.Stderr)
	defer cleanup()
	code := run(ctx, os.Args[1:])
	cleanup()
	os.Exit(code)
}
func emit(v any) error { e := json.NewEncoder(os.Stdout); e.SetIndent("", "  "); return e.Encode(v) }
func readJSON(path string, v any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if e != nil {
		return e
	}
	return strictJSON(b, v)
}
func run(ctx context.Context, args []string) int {
	operation := "review.run"
	if len(args) > 0 && args[0] == "release" {
		operation = "release.run"
	}
	code := 0
	_ = telemetry.Run(ctx, operation, "local", "none", func(ctx context.Context) error {
		code = runCommand(ctx, args)
		if code == 1 {
			return fmt.Errorf("command failed")
		}
		return nil
	})
	return code
}
func runCommand(ctx context.Context, args []string) int {
	if handled, code := handleForgeReview(ctx, args); handled {
		return code
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: reviewd context|journal|review|assess|release|explain|evaluate|serve|backup|restore|publish|comment|revalidate|doctor (shadow mode; use -h)")
		return 2
	}
	var err error
	switch args[0] {
	case "publish", "comment", "revalidate", "doctor":
		command := map[string]func(context.Context, []string) (int, error){"publish": publishCommand, "comment": commentCommand, "revalidate": revalidateCommand, "doctor": doctorCommand}[args[0]]
		code, e := command(ctx, args[1:])
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			if code == 0 {
				code = 1
			}
		}
		return code
	case "context", "journal", "explain", "evaluate", "serve", "backup", "restore":
		code, e := extraCommand(ctx, args)
		if e != nil {
			fmt.Fprintln(os.Stderr, e)
			if code == 0 {
				code = 1
			}
		}
		return code
	case "review":
		f := flag.NewFlagSet("review", flag.ContinueOnError)
		repo := f.String("repo", ".", "local repository")
		base := f.String("base", "", "base revision")
		head := f.String("head", "", "head revision")
		response := f.String("response", "", "saved fake model response JSON")
		modelConfig := f.String("model-config", "", "strict live HTTP model config JSON")
		state := f.String("state", ".reviewd", "durable state directory")
		minimum := f.Int("minimum", 1, "trusted minimum risk")
		threshold := f.Int("threshold", 3, "inclusive advisory threshold")
		cap := f.Int("max-bytes", 1048576, "prompt byte cap")
		policyPath := f.String("policy", "", "trusted flat merge-risk policy JSON")
		if f.Parse(args[1:]) != nil {
			return 2
		}
		if *base == "" || *head == "" || ((*response == "") == (*modelConfig == "")) {
			return 2
		}
		d, e := (gitreader.Reader{Repo: *repo}).Diff(ctx, *base, *head)
		if e != nil {
			err = e
			break
		}
		runID := identityID(struct {
			Base, Head string
			At         int64
		}{d.Base, d.Head, time.Now().UnixNano()})
		m, closeModel, e := modelFor(ctx, *modelConfig, *response, *state, runID)
		if e != nil {
			err = e
			break
		}
		defer closeModel()
		var floorDecision *riskpolicy.Decision
		if *policyPath != "" {
			decision, pe := evaluateDiffPolicy(ctx, *policyPath, d)
			if pe != nil {
				fmt.Fprintln(os.Stderr, pe)
				return 2
			}
			floorDecision = &decision
			*minimum = max(*minimum, decision.EnforcedMinimum)
			*threshold = min(*threshold, decision.Threshold)
		}
		items, e := reviewContext(ctx, *state, *repo, d.Head, d.Patch)
		if e != nil {
			err = e
			break
		}
		hostItem, rubric, e := hostPolicyItem(*minimum, floorDecision, d, *policyPath)
		if e != nil {
			err = e
			break
		}
		items = append(items, hostItem)
		engine := review.Engine{Model: identityModel{m, identityID(struct {
			Repo, Base, Head, Rubric string
			Minimum                  int
		}{*repo, d.Base, d.Head, rubric, *minimum})}}
		r, e := engine.Run(ctx, review.Input{Items: items, Limits: contextpack.Limits{MaxBytes: *cap, MaxTokens: *cap + 4096, ReserveTokens: 4096}, EvidenceIDs: []string{"diff", "host_policy"}, Minimum: *minimum, Threshold: *threshold, RequirementsPass: false, Anchors: d.Anchors})
		if e != nil {
			err = e
			break
		}
		if floorDecision != nil {
			for _, id := range floorDecision.MatchedRules {
				r.Reasons = append(r.Reasons, "floor:"+id)
			}
			if floorDecision.HumanRequired {
				r.Reasons = append(r.Reasons, "policy_requires_human_review")
			}
		}
		err = saveAndAnnounce(ctx, *state, "review", r)
		if err == nil {
			err = emit(r)
		}
		if err == nil && r.Status != "completed" {
			return 4
		}
	case "assess":
		f := flag.NewFlagSet("assess", flag.ContinueOnError)
		path := f.String("run", "", "saved review result JSON or durable ID")
		state := f.String("state", ".reviewd", "durable state directory")
		minimum := f.Int("minimum", 1, "trusted minimum")
		threshold := f.Int("threshold", 3, "inclusive threshold")
		if f.Parse(args[1:]) != nil {
			return 2
		}
		var r review.Result
		if e := loadRun(ctx, *state, *path, &r); e != nil {
			err = e
			break
		}
		r, err = review.Assess(r, *minimum, *threshold, false)
		if err == nil {
			err = emit(r)
		}
	case "release":
		if len(args) < 2 {
			return 2
		}
		f := flag.NewFlagSet("release", flag.ContinueOnError)
		input := f.String("input", "", "captured release.Input JSON")
		response := f.String("response", "", "saved model JSON")
		modelConfig := f.String("model-config", "", "live model config")
		state := f.String("state", ".reviewd", "durable state directory")
		repo := f.String("repo", "", "local Git repository")
		baseline := f.String("baseline", "", "baseline revision")
		candidate := f.String("candidate", "", "candidate revision")
		branch := f.String("branch", "main", "tracked journal branch identity")
		ops := f.String("operational-evidence", "", "strict candidate-bound operational evidence JSON")
		producers := f.String("trusted-producers", "", "comma-separated host-approved evidence producers")
		build := f.String("build-id", "", "trusted build identity")
		policy := f.String("policy", "", "release.Policy JSON")
		assessment := f.String("assessment", "", "saved release.Result JSON")
		format := f.String("format", "markdown", "json|markdown|html|draft")
		if f.Parse(args[2:]) != nil {
			return 2
		}
		if args[1] == "assess" {
			var in release.Input
			var p release.Policy
			if *repo != "" {
				if *baseline == "" || *candidate == "" || *input != "" {
					return 2
				}
				in, err = localReleaseInput(ctx, *repo, *baseline, *candidate, *branch, *state)
				if err != nil {
					break
				}
			} else if e := readJSON(*input, &in); e != nil {
				err = e
				break
			}
			if *modelConfig != "" {
				in.RequireClassifiedItems = true
			}
			if *ops != "" {
				in.Identity.BuildID = *build
				file, e := os.Open(*ops)
				if e != nil {
					err = e
					break
				}
				pack, e := release.ReadOperationalEvidence(ctx, file, release.EvidenceOptions{CandidateOID: in.Identity.CandidateOID, BuildID: *build, Now: time.Now(), MaxAge: 24 * time.Hour, TrustedProducers: strings.Split(*producers, ","), MaxBytes: 1 << 20})
				file.Close()
				if e != nil {
					err = e
					break
				}
				in.Evidence = append(in.Evidence, pack.Evidence...)
				in.Identity.BuildCandidateOID = pack.Document.BuildCandidateOID
			}
			if *policy != "" {
				if e := readJSON(*policy, &p); e != nil {
					err = e
					break
				}
			}
			m, closeModel, e := modelFor(ctx, *modelConfig, *response, *state, identityID(struct {
				Identity release.Identity
				At       int64
			}{in.Identity, time.Now().UnixNano()}))
			if e != nil {
				err = e
				break
			}
			defer closeModel()
			r, e := release.Assess(ctx, in, p, releaseModel{m})
			if e != nil {
				err = e
				break
			}
			err = saveAndAnnounce(ctx, *state, "release", r)
			if err == nil {
				err = emit(r)
			}
			if err == nil && r.AssessmentStatus != "completed" {
				return 4
			}
		} else if args[1] == "render" {
			var r release.Result
			if e := loadRun(ctx, *state, *assessment, &r); e != nil {
				err = e
				break
			}
			out, e := report.RenderRelease(r)
			if e != nil {
				err = e
				break
			}
			switch *format {
			case "json":
				fmt.Println(out.JSON)
			case "markdown":
				fmt.Println(out.Markdown)
			case "html":
				fmt.Println(out.HTML)
			case "draft":
				fmt.Println(out.ChannelDraft)
			default:
				return 2
			}
		} else {
			return 2
		}
	default:
		return 2
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

type savedRelease struct{ body []byte }

func (s savedRelease) Complete(ctx context.Context, _ release.Request) (release.Response, error) {
	if e := ctx.Err(); e != nil {
		return release.Response{}, e
	}
	return release.Response{Body: s.body}, nil
}

// evaluateDiffPolicy uses the complete unfiltered manifest; history is unknown
// until a verified ancestry-bound provider history is supplied.
func evaluateDiffPolicy(ctx context.Context, path string, d gitreader.Diff) (riskpolicy.Decision, error) {
	f, e := os.Open(path)
	if e != nil {
		return riskpolicy.Decision{}, e
	}
	defer f.Close()
	p, e := riskpolicy.Decode(f)
	if e != nil {
		return riskpolicy.Decision{}, e
	}
	facts := riskpolicy.Facts{PolicyTrusted: true, RevisionsCurrent: true}
	for _, c := range d.Files {
		known := !c.Binary && c.Additions >= 0 && c.Deletions >= 0
		facts.Changes = append(facts.Changes, riskpolicy.Change{OldPath: c.OldPath, NewPath: c.NewPath, Added: max(0, c.Additions), Deleted: max(0, c.Deletions), LinesKnown: known})
	}
	return riskpolicy.Evaluate(ctx, p, facts)
}

type releaseModel struct{ m model.Model }

func (m releaseModel) Complete(ctx context.Context, r release.Request) (release.Response, error) {
	v, e := m.m.Complete(ctx, model.Request{Prompt: r.Prompt, Schema: model.ReleaseSchema(), InputIdentity: identityID(r.Identity)})
	tools := 0
	if v.ToolCalls {
		tools = 1
	}
	return release.Response{Body: v.Raw, Truncated: v.Truncated, Refused: v.Refused, ToolCalls: tools}, e
}

type identityModel struct {
	m        model.Model
	identity string
}

func (m identityModel) Complete(ctx context.Context, r model.Request) (model.Response, error) {
	r.InputIdentity = m.identity
	return m.m.Complete(ctx, r)
}

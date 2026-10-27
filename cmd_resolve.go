package main

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"golang.org/x/term"

	"lichen/internal/core"
	"lichen/internal/files"
)

func cmdResolve(args []string) error {
	var p, keep string
	var into []string
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--keep", "--into":
			if i++; i >= len(args) {
				return fmt.Errorf("%s needs a value", a)
			}
			if a == "--keep" {
				keep = args[i]
			} else {
				into = append(into, args[i])
			}
		default:
			if strings.HasPrefix(a, "-") || p != "" {
				return usage("resolve", "")
			}
			p = a
		}
	}
	if (p == "") != (keep == "" && into == nil) {
		return usage("resolve", "")
	}
	if p != "" {
		return resolve(p, keep, into)
	}

	holds := files.Holds()
	if len(holds) == 0 {
		fmt.Println("nothing to resolve")
		return nil
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return usage("resolve", "")
	}
	env, err := core.Load(clilog())
	if err != nil {
		return err
	}
	in := bufio.NewReader(os.Stdin)
	for _, h := range holds {
		keep, into, err := ask(env, in, h.Path)
		if errors.Is(err, files.ErrNotHeld) {
			continue // settled meanwhile
		}
		if err != nil {
			return err
		}
		if keep == "" && into == nil {
			fmt.Println(dim("skipped"))
			continue
		}
		if err := resolve(h.Path, keep, into); err != nil && !errors.Is(err, files.ErrNotHeld) {
			return err
		}
	}
	return nil
}

func resolve(p, keep string, into []string) error {
	how := "placing its new lines"
	if keep != "" {
		how = "keeping " + keep
	}
	if err := change(fmt.Sprintf("resolve %s, %s", tilde(p), how), func(env *core.Env) error {
		return files.Resolve(env, p, keep, into)
	}); err != nil {
		return err
	}
	fmt.Printf("resolved %s\n", tilde(p))
	return nil
}

// ask walks through what holds the edit at p: where its new lines go,
// then whether it wins a conflict. Nothing (an empty answer) skips it.
func ask(env *core.Env, in *bufio.Reader, p string) (keep string, into []string, err error) {
	pd, err := files.Inspect(env, p, nil)
	if err != nil {
		return "", nil, err
	}
	for _, q := range pd.Questions {
		fmt.Printf("\n%s: which source do these new lines go in?\n", bold(p))
		for _, l := range q.Lines {
			fmt.Println(paint("32", "+ "+l))
		}
		for i, s := range q.Sources {
			fmt.Printf("  %d  %s\n", i+1, s)
		}
		fmt.Printf("  d  drop every edit to this file, for the synced version\n")
		a := prompt(in, fmt.Sprintf("1-%d or d: ", len(q.Sources)))
		if a == "" {
			return "", nil, nil
		}
		if a == "d" {
			return "synced", nil, nil
		}
		n, err := strconv.Atoi(a)
		if err != nil || n < 1 || n > len(q.Sources) {
			return "", nil, fmt.Errorf("%q is not one of the choices", a)
		}
		into = append(into, q.Sources[n-1])
	}
	if len(pd.Questions) > 0 {
		if pd, err = files.Inspect(env, p, into); err != nil {
			return "", nil, err
		}
	}
	if pd.Conflict == "" {
		return "", into, nil
	}
	fmt.Printf("\n%s: %s\n", bold(p), pd.Conflict)
	d, err := files.Diff(env, p, useColor)
	if err != nil {
		fmt.Println(dim("(nothing to do here until its sources in ~/lichen/files compile)"))
		return "", nil, nil
	}
	os.Stdout.Write(d)
	q, answers := "keep (h)ere or (s)ynced: ", "hs"
	if !pd.Keepable {
		q, answers = "this edit can't be kept as it is: drop it for the (s)ynced version: ", "s"
	}
	for {
		switch a := prompt(in, q); {
		case a == "":
			return "", nil, nil
		case len(a) == 1 && strings.Contains(answers, a):
			return map[string]string{"h": "here", "s": "synced"}[a], into, nil
		}
	}
}

// prompt asks q, and reads the answer: nothing, at the end of the input.
func prompt(in *bufio.Reader, q string) string {
	fmt.Print(q)
	a, _ := in.ReadString('\n')
	return strings.TrimSpace(a)
}

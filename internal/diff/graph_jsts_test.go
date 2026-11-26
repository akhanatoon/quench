package diff

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeTestFile(t *testing.T, root, rel, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", rel, err)
	}
	if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
}

func TestExpandJSReverseImporters_RelativeImportsAndOneHop(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/math.ts", `export const add = (a: number, b: number) => a + b;`)
	writeTestFile(t, dir, "src/calc.ts", `import { add } from './math';
export const double = (x: number) => add(x, x);`)
	writeTestFile(t, dir, "src/reexport.ts", `export { add } from './math';`)
	writeTestFile(t, dir, "src/legacy.cjs", `const { add } = require('./math');
module.exports = { add };`)
	writeTestFile(t, dir, "src/nested/consumer.tsx", `import {
  add,
} from '../math';
export const View = () => add(1, 2);`)
	// 2-hop importer IS now included via transitive BFS
	writeTestFile(t, dir, "src/app.ts", `import { double } from './calc';
console.log(double(2));`)
	// Unrelated file should NOT be included
	writeTestFile(t, dir, "src/other.ts", `import React from 'react';
export const x = 1;`)

	got := ExpandJSReverseImporters(dir, []string{"src/math.ts"})
	want := []string{
		"src/app.ts",
		"src/calc.ts",
		"src/legacy.cjs",
		"src/math.ts",
		"src/nested/consumer.tsx",
		"src/reexport.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_TSESMJsToTsResolution(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/utils.ts", `export function fmt(v: string) { return v.trim(); }`)
	writeTestFile(t, dir, "src/Button.tsx", `export function Button() { return null; }`)
	writeTestFile(t, dir, "src/service.mts", `import { fmt } from './utils.js';
export const run = () => fmt('ok');`)
	writeTestFile(t, dir, "src/page.ts", `import { Button } from './Button.js';
export const Page = Button;`)

	got := ExpandJSReverseImporters(dir, []string{"src/utils.ts", "src/Button.tsx"})
	want := []string{
		"src/Button.tsx",
		"src/page.ts",
		"src/service.mts",
		"src/utils.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_BarrelIndexResolution(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/components/index.ts", `export const Card = () => null;`)
	writeTestFile(t, dir, "src/App.tsx", `import { Card } from './components';
export const App = () => Card();`)
	writeTestFile(t, dir, "src/components/sibling.ts", `import { Card } from '.';
export const Sibling = Card;`)

	got := ExpandJSReverseImporters(dir, []string{"src/components/index.ts"})
	want := []string{
		"src/App.tsx",
		"src/components/index.ts",
		"src/components/sibling.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_SkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "src/core.ts", `export const core = 1;`)
	writeTestFile(t, dir, "src/main.ts", `import { core } from './core';`)
	writeTestFile(t, dir, "node_modules/pkg/index.js", `const { core } = require('../../src/core');`)
	writeTestFile(t, dir, "dist/main.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, "build/main.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, ".next/chunk.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, "coverage/report.js", `import { core } from '../src/core';`)
	writeTestFile(t, dir, "vendor/ext.js", `import { core } from '../src/core';`)

	got := ExpandJSReverseImporters(dir, []string{"src/core.ts"})
	want := []string{
		"src/core.ts",
		"src/main.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_EmptyInput(t *testing.T) {
	dir := t.TempDir()
	if got := ExpandJSReverseImporters(dir, nil); got != nil {
		t.Fatalf("expected nil for empty changedFiles, got %v", got)
	}
}

func TestExpandJSReverseImporters_TSConfigPathAliasesAndJSONC(t *testing.T) {
	dir := t.TempDir()

	// JSONC with // line comments, /* block comments */, trailing commas, and /* inside string literals
	writeTestFile(t, dir, "tsconfig.json", `{
  // Root TypeScript configuration
  "compilerOptions": {
    /* Base URL defaults to "." when omitted, set explicitly here */
    "baseUrl": ".",
    "paths": {
      "@/*": ["./src/*"], // inline comment after trailing comma
      "@components/*": ["src/components/*",],
    },
  },
}`)

	// 2-hop chain via @/* alias:
	// src/lib/auth.ts <- src/services/session.ts (via @/lib/auth) <- tests/auth.test.ts (via @/services/session)
	writeTestFile(t, dir, "src/lib/auth.ts", `export function verifyToken(t: string) { return t.length > 0; }`)
	writeTestFile(t, dir, "src/services/session.ts", `import { verifyToken } from '@/lib/auth';
export const checkSession = (t: string) => verifyToken(t);`)
	writeTestFile(t, dir, "tests/auth.test.ts", `import { checkSession } from '@/services/session';
expect(checkSession('abc')).toBe(true);`)

	// Component alias + barrel index resolution
	writeTestFile(t, dir, "src/components/Button/index.tsx", `import { verifyToken } from '@/lib/auth';
export const Button = () => verifyToken('x');`)
	writeTestFile(t, dir, "src/pages/Login.tsx", `import { Button } from '@components/Button';
export const Login = () => Button();`)

	// Unrelated external package import should NOT be included
	writeTestFile(t, dir, "src/unrelated.ts", `import React from 'react';
export const y = 2;`)

	got := ExpandJSReverseImporters(dir, []string{"src/lib/auth.ts"})
	want := []string{
		"src/components/Button/index.tsx",
		"src/lib/auth.ts",
		"src/pages/Login.tsx",
		"src/services/session.ts",
		"tests/auth.test.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

func TestExpandJSReverseImporters_JSConfigFallbackAndBaseURL(t *testing.T) {
	dir := t.TempDir()

	writeTestFile(t, dir, "jsconfig.json", `{
  /* jsconfig with non-root baseUrl */
  "compilerOptions": {
    "baseUrl": "src",
    "paths": {
      "@utils/*": ["utils/*"],
    }
  }
}`)

	writeTestFile(t, dir, "src/utils/format.js", `export const fmt = (s) => s.trim();`)
	writeTestFile(t, dir, "src/app.jsx", `import { fmt } from '@utils/format';
export const App = () => fmt('hi');`)

	got := ExpandJSReverseImporters(dir, []string{"src/utils/format.js"})
	want := []string{
		"src/app.jsx",
		"src/utils/format.js",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ExpandJSReverseImporters() =\n%v\nwant:\n%v", got, want)
	}
}

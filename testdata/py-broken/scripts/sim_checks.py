import importlib.util
import pathlib
import sys

mode = sys.argv[1]
targets = sys.argv[2:]

if mode == "lint":
    failed = False
    for path in targets:
        text = pathlib.Path(path).read_text(encoding="utf-8")
        for idx, line in enumerate(text.splitlines(), start=1):
            if "unused_var" in line:
                print(f"{path}:{idx}:5: F841 Local variable `unused_var` is assigned to but never used")
                failed = True
    sys.exit(1 if failed else 0)

if mode == "pytest":
    sys.path.insert(0, str(pathlib.Path.cwd()))
    failed = False
    for target in targets:
        p = pathlib.Path(target)
        files = [p] if p.is_file() else sorted(p.rglob("test_*.py"))
        for tf in files:
            rel = tf.as_posix()
            spec = importlib.util.spec_from_file_location(tf.stem, tf)
            mod = importlib.util.module_from_spec(spec)
            spec.loader.exec_module(mod)
            for name in dir(mod):
                if name.startswith("test_") and callable(getattr(mod, name)):
                    fn = getattr(mod, name)
                    try:
                        fn()
                    except AssertionError as exc:
                        lineno = fn.__code__.co_firstlineno + 1
                        msg = str(exc) or "assertion failed"
                        print(f"E       AssertionError: {msg}")
                        print(f"{rel}:{lineno}: AssertionError")
                        print(f"FAILED {rel}::{name} - AssertionError: {msg}")
                        failed = True
    sys.exit(1 if failed else 0)

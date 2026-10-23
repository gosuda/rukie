/* Hooks bindings to preact/hooks, typed per call site. The setter shape and
the option cleanup follow the rescript-lang/rescript-react contract, pointed
at the preact/hooks module instead of react. */

@module("preact/hooks")
external useState: (unit => 'state) => ('state, ('state => 'state) => unit) = "useState"

@module("preact/hooks")
external useEffectOnEveryRender: (unit => option<unit => unit>) => unit = "useEffect"

@module("preact/hooks")
external useEffect: (unit => option<unit => unit>, 'deps) => unit = "useEffect"

@module("preact/hooks")
external useEffect0: (unit => option<unit => unit>, @as(json`[]`) _) => unit = "useEffect"

@module("preact/hooks")
external useEffect1: (unit => option<unit => unit>, array<'a>) => unit = "useEffect"

@module("preact/hooks")
external useMemo: (unit => 'any, 'deps) => 'any = "useMemo"

@module("preact/hooks")
external useMemo0: (unit => 'any, @as(json`[]`) _) => 'any = "useMemo"

@module("preact/hooks")
external useMemo1: (unit => 'any, array<'a>) => 'any = "useMemo"

type ref<'value> = {mutable current: 'value}

@module("preact/hooks")
external useRef: 'value => ref<'value> = "useRef"

@module("preact/hooks")
external useCallback: ('callback, 'deps) => 'callback = "useCallback"

@module("preact/hooks")
external useCallback0: ('callback, @as(json`[]`) _) => 'callback = "useCallback"

@module("preact/hooks")
external useCallback1: ('callback, array<'a>) => 'callback = "useCallback"

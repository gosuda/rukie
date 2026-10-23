// App.res
// Hydration seed: render the counter component into every subtree the
// fallback HTML marks with `data-rukie-app`. Serialized op-table props ride
// `data-props` JSON on the marked subtree; ENG-65 grows the real decoders.

open Preact

@module("preact") external render: (Jsx.element, Dom.element) => unit = "render"

@send external getAttribute: (Dom.element, string) => option<string> = "getAttribute"

@val external document: Dom.element = "document"

@send external querySelectorAll: (Dom.element, string) => Dom.nodeList = "querySelectorAll"

@send external forEachNode: (Dom.nodeList, @uncurried (Dom.element => unit)) => unit =
  "forEach"

type props = {serialized: string}

let make = (props: props): Jsx.element => {
  let (count, setCount) = PreactHooks.useState(() => 0)
  let countEl = int(count)
  <button onClick={_ => setCount(c => c + 1)}>
    {string(props.serialized)} {countEl}
  </button>
}

let hydrateOne = (el: Dom.element): unit => {
  let serialized = el->getAttribute("data-props")->Option.getOr("{}")
  Preact.jsx(make, {serialized: serialized})->render(el)
}

let main = () => {
  document->querySelectorAll("[data-rukie-app]")->forEachNode(hydrateOne)
}

main()

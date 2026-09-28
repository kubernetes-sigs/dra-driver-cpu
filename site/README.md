# Documentation site

The documentation is published as a website with [Hugo
(extended)](https://gohugo.io/) and the [Docsy](https://www.docsy.dev/) theme,
built and hosted on Netlify at <https://dra-driver-cpu.sigs.k8s.io>.

## How the documentation is assembled

The prose is authored in [`../docs`](../docs), where it is also read on GitHub.
It does not move and is not copied. Two pieces connect it to the site:

- `hugo.toml` mounts `../docs` into the assets of this site, under
  `assets/additional/docs`, so the files are available to templates without
  becoming part of the content tree;

- each page under `content/docs/` carries the navigation metadata for one
  documentation page and pulls its prose in with the
  [`include-file`](layouts/shortcodes/include-file.html) shortcode:

  ```markdown
  ---
  title: Quickstart
  weight: 10
  ---

  {{% include-file file="additional/docs/user/quickstart.md" %}}
  ```

The shortcode drops the level-one heading of the included file, because the
page title comes from the front matter, and leaves an empty anchor with the id
that heading would have had, so links to it keep working. It also rewrites
links that leave the documentation, such as the one to
`hack/examples/pod_with_resource_claim_node_allocatable.yaml`, to their URL on
GitHub. Links between documentation pages are left to Hugo, which resolves them
in the context of the including page; that works because the tree under
`content/docs/` mirrors the layout of `docs/`.

[`../README.md`](../README.md) is published the same way, as the Overview page
(`content/docs/overview.md`), which also makes it the first entry in the
Documentation navigation. It sits at the repository root rather than in `docs/`,
so `content/docs/overview.md` passes `from="repo-root"` to the shortcode: a link
into `docs/` becomes a link to the published page, and every other relative link
becomes a link to the file on GitHub, because the site does not publish it. The
home page (`content/_index.md`) redirects to `/docs/overview/`.

The section indexes under `content/` are site-only pages.

## Adding or moving a documentation page

1. write the page as `../docs/user/<name>.md` or `../docs/dev/<name>.md`;
1. add `content/docs/user/<name>.md` or `content/docs/dev/<name>.md` with the
   `title` and the `weight` that place it in the navigation, and a line that
   includes the file from `docs/`.
1. when the page starts a new section, copy the `github_subdir` and
   `path_base_for_github_subdir` front matter from an existing
   `content/docs/<section>/_index.md`; without it the "Edit this page" links of
   the new section point at the wrapper page instead of the prose.

## Build and serve locally

Hugo extended 0.157.0 and Go are required, plus Node.js for the Docsy PostCSS
step. That is the version `netlify.toml` pins: Hugo 0.161 and later run PostCSS
with Node's `--permission` flag and need Node 22 or later, and 0.158 and later
deprecate an API that Docsy v0.15.0 still uses.

From the repository root, the Makefile wraps both commands:

```sh
make site-serve   # serve the site at http://localhost:1313/
make site-build   # or build it into site/public
```

Both install the Node.js dependencies on the first run.

From this directory, the same thing by hand:

```sh
npm ci          # needed for the PostCSS step of a production build
hugo server     # or: hugo --gc --minify, which writes site/public
```

Run Hugo from this directory, not from the repository root, where it would
build an empty site.

Netlify builds with `base = "site"`; see
[`../netlify.toml`](../netlify.toml).

## Note on "View this page" and "Edit this page"

Docsy derives those links from the file behind the page. For the pages that
mirror `docs/`, a front-matter cascade on `content/docs/<section>/_index.md`
rewrites that path, so the links open the file in `docs/` and editing a page
edits the prose rather than the wrapper. The Overview page names `README.md` in
its own front matter, for the same reason. The section indexes have no
counterpart in `docs/`, so they keep the default, which names the page under
`content/`; the index also keeps `github_subdir: "site"` because the cascade
reaches it as well.

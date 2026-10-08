# Wiki content

These pages mirror the project wiki. GitHub wikis are only available on public
repositories (or private repositories on paid plans), so while this repository
is **private** the pages live here under version control.

## Publishing to the GitHub wiki

Once the repository is public (or wikis are enabled), publish these to the wiki
with:

```sh
# enable the wiki first (Settings > Features > Wikis, or:)
gh api -X PATCH repos/m1r3dk/dirhop -f has_wiki=true

# create the first page in the browser once, then:
git clone git@github.com:m1r3dk/dirhop.wiki.git
cp docs/wiki/*.md dirhop.wiki/
cd dirhop.wiki && git add -A && git commit -m "wiki: sync from docs/wiki" && git push
```

`Home.md` is the landing page; `_Sidebar.md` and `_Footer.md` are the wiki
chrome. Internal `[[Page]]` links resolve against wiki page names.

## Pages

- `Home.md`
- `Installation.md`
- `Quick-Start.md`
- `Command-Reference.md`
- `Recipes.md`
- `Flags-and-Exit-Codes.md`
- `Commands.md` (redirect to `Command-Reference.md`)
- `Buckets.md`
- `GrayHatWarfare.md`
- `Configuration.md`
- `Troubleshooting.md`
- `Architecture.md`
- `Security.md`
- `FAQ.md`

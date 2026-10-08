# Wiki content

These Markdown pages are the source of truth for the project wiki, kept under
version control here. The repository is public and the GitHub wiki is enabled.

## Publishing to the GitHub wiki

The wiki is a separate git repo (`dirhop.wiki.git`). GitHub only creates it
after the first page is saved once in the browser: open the
[Wiki tab](https://github.com/m1r3dk/dirhop/wiki) and save any page. After that,
sync these files with:

```sh
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

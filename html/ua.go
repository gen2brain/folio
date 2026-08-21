package html

import "sync"

// UserAgent returns the user agent stylesheet, restricted to the properties
// the engine reads.
func UserAgent() *Stylesheet { return uaSheet() }

var uaSheet = sync.OnceValue(func() *Stylesheet { return ParseCSS([]byte(uaCSS), OriginUA) })

const uaCSS = `
html, body, address, article, aside, blockquote, center, dd, details, dialog,
dir, div, dl, dt, fieldset, figcaption, figure, footer, form, h1, h2, h3, h4,
h5, h6, header, hgroup, hr, legend, listing, main, menu, nav, ol, p, plaintext,
pre, section, summary, ul, xmp, table, caption, thead, tbody, tfoot, tr, td, th
	{ display: block }

head, link, meta, script, style, title, base, template, datalist, param,
source, track, rp { display: none }
[hidden] { display: none }

body { margin: 8px }

p, dl, multicol { margin: 1em 0 }
blockquote, figure { margin: 1em 40px }
dd { margin-left: 40px }
ol, ul, menu, dir { margin: 1em 0; padding-left: 40px }

h1 { font-size: 2em;    font-weight: bold; margin: 0.67em 0 }
h2 { font-size: 1.5em;  font-weight: bold; margin: 0.83em 0 }
h3 { font-size: 1.17em; font-weight: bold; margin: 1em 0 }
h4 { font-size: 1em;    font-weight: bold; margin: 1.33em 0 }
h5 { font-size: 0.83em; font-weight: bold; margin: 1.67em 0 }
h6 { font-size: 0.67em; font-weight: bold; margin: 2.33em 0 }

ul, menu, dir { list-style-type: disc }
ol { list-style-type: decimal }
li { display: list-item }
ul ul, ol ul, ul ol, ol ol { margin-top: 0; margin-bottom: 0 }

pre, xmp, plaintext, listing { font-family: monospace; white-space: pre; margin: 1em 0 }
code, kbd, samp, tt { font-family: monospace }

b, strong, th { font-weight: bolder }
i, cite, em, var, address, dfn { font-style: italic }
th { text-align: center }
center, caption { text-align: center }

u, ins { text-decoration: underline }
s, strike, del { text-decoration: line-through }
mark { background-color: yellow; color: black }
big { font-size: larger }
small, sub, sup { font-size: smaller }
sub { vertical-align: sub }
sup { vertical-align: super }
rt { font-size: 50% }

a[href] { color: #0000ee; text-decoration: underline }

hr { display: block; margin: 0.5em auto; height: 1px; background-color: gray }

nobr { white-space: nowrap }
`

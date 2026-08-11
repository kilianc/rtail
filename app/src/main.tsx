// Styles are compiled separately by tools/build.js (dart-sass) and linked from
// index.html, so there is no stylesheet import here.
import { render } from 'preact'
import { App } from './app.js'

const root = document.getElementById('root')

if (!root) throw new Error('missing #root')

render(<App />, root)

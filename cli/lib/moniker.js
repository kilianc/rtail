/*!
 * moniker.js — random, human-readable stream names.
 *
 * Replaces the `moniker` package, which was last published in 2013 and pulled
 * in its own word-list files just to produce a two-word label.
 */

const ADJECTIVES = [
  'amber', 'ancient', 'autumn', 'bitter', 'bold', 'brave', 'calm', 'cold',
  'crimson', 'damp', 'dawn', 'divine', 'dry', 'empty', 'falling', 'floral',
  'frosty', 'gentle', 'green', 'hidden', 'holy', 'icy', 'late', 'lingering',
  'little', 'lively', 'long', 'loud', 'misty', 'morning', 'muddy', 'nameless',
  'old', 'patient', 'plain', 'polished', 'proud', 'purple', 'quiet', 'rapid',
  'restless', 'rough', 'round', 'shy', 'silent', 'small', 'snowy', 'solitary',
  'sparkling', 'spring', 'still', 'summer', 'throbbing', 'tight', 'twilight',
  'wandering', 'weathered', 'white', 'wild', 'winter', 'wispy', 'withered'
]

const NOUNS = [
  'band', 'bar', 'base', 'bird', 'block', 'boat', 'bonus', 'bread', 'breeze',
  'brook', 'bush', 'butterfly', 'cake', 'cell', 'cherry', 'cloud', 'credit',
  'darkness', 'dawn', 'dew', 'disk', 'dream', 'dust', 'feather', 'field',
  'fire', 'firefly', 'flower', 'fog', 'forest', 'frog', 'frost', 'glade',
  'glitter', 'grass', 'hall', 'hat', 'haze', 'heart', 'hill', 'king', 'lab',
  'lake', 'leaf', 'limit', 'math', 'meadow', 'mode', 'moon', 'morning',
  'mountain', 'mouse', 'mud', 'night', 'paper', 'pine', 'poetry', 'pond',
  'queen', 'rain', 'recipe', 'resonance', 'rice', 'river', 'salad', 'scene',
  'sea', 'shadow', 'shape', 'silence', 'sky', 'smoke', 'snow', 'snowflake',
  'sound', 'star', 'sun', 'sunset', 'surf', 'term', 'thunder', 'tooth',
  'tree', 'truth', 'union', 'unit', 'violet', 'voice', 'water', 'waterfall',
  'wave', 'wildflower', 'wind', 'wood'
]

function pick(list) {
  return list[Math.floor(Math.random() * list.length)]
}

/**
 * @returns {string} a name like "wandering-brook"
 */
export function choose() {
  return pick(ADJECTIVES) + '-' + pick(NOUNS)
}

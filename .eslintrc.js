module.exports = {
    extends: [
        'airbnb-base',
        'plugin:vue/base',
        'plugin:vue/essential',
        'plugin:vue/strongly-recommended',
        'plugin:vue/recommended',
    ],
    parserOptions: {
        parser: 'babel-eslint',
        ecmaVersion: 2017,
        sourceType: 'module',
    },
    "env": {
        "node": true,
        "mocha": true,
        "es6": true,
        "browser": true
    },
    "rules": {
        "import/extensions": [
            "error",
            "always",
            {
                "js": "never",
            }
        ],
        "indent": [
            "error",
            2,
            {
                "SwitchCase": 1
            }
        ],
        "no-unused-vars": [
            2,
            {
                "vars": "local",
                "args": "none"
            }
        ],
        "func-names": "off",
        "import/no-dynamic-require": "off",
        "import/no-extraneous-dependencies": [
            "off",
            {
                "devDependencies": false
            }
        ],
        "max-len": [
            "error",
            125,
            2,
            {
                "ignoreUrls": true,
                "ignoreComments": false,
                "ignoreRegExpLiterals": true,
                "ignoreStrings": true,
                "ignoreTemplateLiterals": true
            }
        ],
        "no-param-reassign": [
            "error",
            {
                "props": false
            }
        ],
        "prefer-destructuring": [
            "error",
            {
                "object": true,
                "array": false
            }
        ],
        "no-bitwise": [
            "off"
        ],
        "no-plusplus": [
            "off"
        ],
        "no-empty": [
            2,
            {
                "allowEmptyCatch": true
            }
        ],
        "comma-dangle": [
            "error",
            {
                "arrays": "always-multiline",
                "objects": "always-multiline",
                "imports": "always-multiline",
                "exports": "always-multiline",
                "functions": "never"
            }
        ]
    }
};

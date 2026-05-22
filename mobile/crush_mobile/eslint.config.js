const { defineConfig } = require('eslint/config')
const expoConfig = require('eslint-config-expo/flat')
const eslintPluginPrettierRecommended = require('eslint-plugin-prettier/recommended')

module.exports = defineConfig([
    expoConfig,
    eslintPluginPrettierRecommended,
    {
        ignores: ['dist/*'],
    },

    {
        files: ['**/*.{js,jsx,ts,tsx,d.ts}'],
        languageOptions: {
            parserOptions: {
                project: './tsconfig.json',
            },
        },
        plugins: {
        },
        rules: {
            'prettier/prettier': [
                'warn',
                {
                    usePrettierrc: true,
                },
            ],
            radix: 'off',
            '@typescript-eslint/no-require-imports': 'off',
            'object-shorthand': ['warn', 'consistent'],
            'import/order': [
                'warn',
                {
                    alphabetize: { order: 'asc', caseInsensitive: true },
                    groups: [['builtin', 'external'], ['internal'], ['parent', 'sibling', 'index']],
                    'newlines-between': 'always',
                },
            ],
        },
    },
])

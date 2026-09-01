module.exports = {
  root: true,
  extends: ['universe/node'],
  plugins: ['node'],
  ignorePatterns: ['bin/', 'dist/'],
  rules: {
    'no-console': 'warn',
    'no-constant-condition': ['warn', { checkLoops: false }],
    'sort-imports': [
      'warn',
      {
        ignoreDeclarationSort: true,
      },
    ],
    curly: 'warn',
    'import/no-cycle': 'error',
    'import/no-extraneous-dependencies': [
      'error',
      { devDependencies: ['**/__tests__/**/*', '**/__mocks__/**/*'] },
    ],
    'import/no-relative-packages': 'error',
    'no-restricted-imports': [
      'error',
      {
        paths: [
          {
            name: 'lodash',
            message: "Don't use lodash, it's heavy!",
          },
        ],
      },
    ],
    'no-underscore-dangle': ['error', { allow: ['__typename'] }],
    'node/no-sync': 'error',
  },
  overrides: [
    {
      files: ['*.ts', '*.d.ts'],
      parserOptions: {
        // Extends tsconfig.json to also cover __tests__, which the build excludes.
        project: './tsconfig.eslint.json',
        tsconfigRootDir: __dirname,
      },
      rules: {
        '@typescript-eslint/explicit-function-return-type': [
          'warn',
          {
            allowExpressions: true,
          },
        ],
        '@typescript-eslint/prefer-nullish-coalescing': ['warn', { ignorePrimitives: true }],
        '@typescript-eslint/no-confusing-void-expression': 'warn',
        '@typescript-eslint/await-thenable': 'error',
        '@typescript-eslint/no-misused-promises': [
          'error',
          {
            checksVoidReturn: false,
          },
        ],
        '@typescript-eslint/no-floating-promises': 'error',
        'no-void': ['warn', { allowAsStatement: true }],
        'no-return-await': 'off',
        '@typescript-eslint/return-await': ['error', 'always'],
        '@typescript-eslint/no-confusing-non-null-assertion': 'warn',
        '@typescript-eslint/no-extra-non-null-assertion': 'warn',
        '@typescript-eslint/prefer-as-const': 'warn',
        '@typescript-eslint/prefer-includes': 'warn',
        '@typescript-eslint/prefer-readonly': 'warn',
        '@typescript-eslint/prefer-string-starts-ends-with': 'warn',
        '@typescript-eslint/prefer-ts-expect-error': 'warn',
        '@typescript-eslint/no-unnecessary-type-assertion': 'warn',
      },
    },
  ],
};

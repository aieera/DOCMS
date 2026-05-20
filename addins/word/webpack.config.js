// Word add-in build pipeline (ADR 0113). Same shape as
// addins/outlook/webpack.config.js — kept structurally identical so
// future "all Office add-ins" build glue can fan out cleanly.

const path = require('path')
const HtmlWebpackPlugin = require('html-webpack-plugin')
const CopyWebpackPlugin = require('copy-webpack-plugin')

module.exports = (env, argv) => {
  const isProd = argv.mode === 'production'
  return {
    entry: {
      taskpane: './src/taskpane/index.tsx',
      commands: './src/commands/commands.ts',
    },
    output: {
      path: path.resolve(__dirname, 'dist'),
      filename: '[name].[contenthash].js',
      clean: true,
    },
    resolve: { extensions: ['.ts', '.tsx', '.js'] },
    module: {
      rules: [
        { test: /\.tsx?$/, use: 'ts-loader', exclude: /node_modules/ },
        { test: /\.css$/, use: ['style-loader', 'css-loader'] },
      ],
    },
    plugins: [
      new HtmlWebpackPlugin({
        filename: 'taskpane.html',
        template: './src/taskpane/taskpane.html',
        chunks: ['taskpane'],
      }),
      new HtmlWebpackPlugin({
        filename: 'commands.html',
        template: './src/commands/commands.html',
        chunks: ['commands'],
      }),
      new CopyWebpackPlugin({
        patterns: [
          { from: 'manifest.xml', to: '.' },
          { from: 'assets',       to: 'assets' },
        ],
      }),
    ],
    devServer: {
      static: { directory: path.join(__dirname, 'dist') },
      port:   3002,                       // distinct from outlook addin (:3001)
      hot:    true,
      server: 'https',
      historyApiFallback: true,
      headers: { 'Access-Control-Allow-Origin': '*' },
    },
    devtool: isProd ? 'source-map' : 'eval-cheap-module-source-map',
  }
}

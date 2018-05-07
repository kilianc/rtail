const path = require('path');
const CleanWebpackPlugin = require('clean-webpack-plugin');
const HtmlWebpackPlugin = require('html-webpack-plugin');
const UglifyJSPlugin = require('uglifyjs-webpack-plugin');

const distPath = path.resolve(__dirname, './dist');

const config = {
  entry: './app/app.js',
  output: {
    filename: 'bundle.js',
    path: distPath,
  },
  module: {
    rules: [{
      test: /\.scss$/,
      use: [
        { loader: 'style-loader' },
        { loader: 'css-loader' },
        { loader: 'sass-loader' },
      ],
    }],
  },
  plugins: [
    new CleanWebpackPlugin([distPath]),
    new HtmlWebpackPlugin({
      template: 'app/index.html',
    }),
  ],
};

if (process.env.NODE_ENV === 'production') {
  config.plugins.push(new UglifyJSPlugin({
    uglifyOptions: {
      mangle: false,
      output: { comments: false },
    },
  }));
}

module.exports = config;

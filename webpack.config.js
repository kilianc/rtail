const webpack = require('webpack');
const path = require('path');
const HtmlWebpackPlugin = require('html-webpack-plugin');
const ExtractTextPlugin = require('extract-text-webpack-plugin'); // separate css
const CopyWebpackPlugin = require('copy-webpack-plugin');

const webpackConfig = {}; // init object
const isProduction = process.env.NODE_ENV === 'production'; // production environment

// input
webpackConfig.entry = {
  app: './src/app.js', // main
};

// output
webpackConfig.output = {
  path: path.resolve(__dirname, 'dist'),
  publicPath: '/',
  filename: isProduction ? '[name].[hash].js' : '[name].js',
};

// loader
webpackConfig.module = {
  rules: [
    {
      test: /\.css$/,
      use: ExtractTextPlugin.extract({
        fallback: 'style-loader',
        use: 'css-loader',
      }),
    },
    {
      test: /\.vue$/,
      loader: 'vue-loader',
    },
    {
      test: /\.js$/,
      loader: 'babel-loader',
      exclude: /node_modules/,
    },
    {
      test: /\.(eot(|\?v=.*)|woff(|\?v=.*)|woff2(|\?v=.*)|ttf(|\?v=.*)|svg(|\?v=.*))$/,
      loader: 'file-loader',
      options: { name: 'fonts/[name].[ext]' },
    },
    {
      test: /\.(png|jpg|gif)$/,
      loader: 'file-loader',
    },
  ],
};

webpackConfig.plugins = [
  new HtmlWebpackPlugin({ template: './src/index.html' }),
  new ExtractTextPlugin({
    filename: isProduction ? 'app.[hash].css' : 'app.css',
  }),
  new webpack.DefinePlugin({
    'process.env': { NODE_ENV: '"production"' },
  }),
  new CopyWebpackPlugin([
    { context: 'src/images', from: '*', to: path.join(__dirname, 'dist', 'images') },
  ]),
];

if (!isProduction) {
  webpackConfig.devServer = {
    contentBase: path.resolve(__dirname, 'dist'),
    compress: true,
    historyApiFallback: true,
  };
}

module.exports = webpackConfig;

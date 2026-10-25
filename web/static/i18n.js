(() => {
  const messages = window.tinyTubeMessages;
  window.tinyTubeTranslate = (message, ...args) => {
    const translated = Object.prototype.hasOwnProperty.call(messages, message) ? messages[message] : message;
    let index = 0;
    return args.length ? translated.replace(/%[sd]/g, () => String(args[index++])) : translated;
  };
})();

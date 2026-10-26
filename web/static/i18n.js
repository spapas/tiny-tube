(() => {
  const messages = window.tinyTubeMessages;
  const locale = document.documentElement.lang || 'en';
  const numbers = new Intl.NumberFormat(locale);
  const plurals = new Map();
  window.tinyTubeTranslate = (id, data = {}) => {
    if (!Object.prototype.hasOwnProperty.call(messages, id)) return id;
    const message = messages[id];
    let form = 'other';
    if (Object.prototype.hasOwnProperty.call(data, 'Count')) {
      // An English fallback must use English plural rules, even when the
      // selected locale has additional forms (e.g. Russian one/few/many).
      const language = message.language || locale;
      if (!plurals.has(language)) plurals.set(language, new Intl.PluralRules(language));
      form = plurals.get(language).select(Number(data.Count));
    }
    return (message[form] || message.other).replace(/\{\{\s*\.([A-Za-z][A-Za-z0-9]*)\s*\}\}/g, (token, name) => {
      if (!Object.prototype.hasOwnProperty.call(data, name)) return token;
      return name === 'Count' ? numbers.format(data[name]) : String(data[name]);
    });
  };
})();

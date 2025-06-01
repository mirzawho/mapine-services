FROM node:22-alpine

RUN apk add --no-cache openssl bind-tools

ENV NODE_ENV=production

RUN mkdir /app
WORKDIR /app

COPY package.json package-lock.json ./

RUN npm install --production --verbose

COPY . .

CMD ["npm", "run", "prod"]